# Copyright 2026 Kushan Shah
# SPDX-License-Identifier: Apache-2.0

import cv2
import numpy as np
import requests
import argparse
import datetime
import concurrent.futures
import sys
import time
import json
import os
import hashlib
import subprocess

WIDTH = 1280
HEIGHT = 720

# Create cache directory for WMS tiles
CACHE_DIR = "cache/wms"
if not os.path.exists(CACHE_DIR):
    os.makedirs(CACHE_DIR, exist_ok=True)

def fetch_wms_image(url_template, layer_name, bbox, date_str, is_base=False):
    """Fetch a WMS image with cryptographic disk caching and retry logic.
    Base layers = JPEG (opaque), overlays = PNG (with alpha).
    Non-GIBS servers skip the TIME param (they serve live data)."""
    base_url = url_template.split("?")[0] if "?" in url_template else url_template
    fmt = "image/jpeg" if is_base else "image/png"
    is_gibs = "gibs.earthdata.nasa.gov" in url_template
    time_param = f"&TIME={date_str}" if (is_gibs and "BlueMarble" not in layer_name) else ""
    url = f"{base_url}?SERVICE=WMS&REQUEST=GetMap&VERSION=1.1.1&LAYERS={layer_name}&SRS=EPSG:4326&BBOX={bbox[0]},{bbox[1]},{bbox[2]},{bbox[3]}&WIDTH={WIDTH}&HEIGHT={HEIGHT}&FORMAT={fmt}{time_param}&TRANSPARENT={'FALSE' if is_base else 'TRUE'}"
    
    # 1. Cryptographic Disk Caching
    url_hash = hashlib.md5(url.encode('utf-8')).hexdigest()
    ext = ".jpg" if is_base else ".png"
    cache_path = os.path.join(CACHE_DIR, f"{url_hash}{ext}")
    
    if os.path.exists(cache_path):
        try:
            with open(cache_path, "rb") as f:
                content = f.read()
            if len(content) > 500:
                arr = np.frombuffer(content, np.uint8)
                img = cv2.imdecode(arr, cv2.IMREAD_UNCHANGED)
                if img is not None:
                    if img.ndim == 2:
                        img = cv2.cvtColor(img, cv2.COLOR_GRAY2BGR)
                    return img
        except Exception as e:
            print(f"[WARN] Failed to load cache for {layer_name}: {e}", file=sys.stderr)

    # 2. Network Fetch if Cache Miss
    max_retries = 3
    for attempt in range(max_retries):
        try:
            timeout = 20 + (attempt * 10)  # 20s, 30s, 40s — progressive timeout
            resp = requests.get(url, headers={'User-Agent': 'GeoStream-CV/1.0'}, timeout=timeout)
            
            if resp.status_code != 200:
                print(f"[WARN] WMS {resp.status_code} for {layer_name} on {date_str} (attempt {attempt+1})", file=sys.stderr)
                continue
                
            # Check if server returned an error XML instead of an image
            content_type = resp.headers.get('Content-Type', '')
            if 'xml' in content_type or 'html' in content_type:
                print(f"[WARN] WMS returned {content_type} for {layer_name} on {date_str}", file=sys.stderr)
                return None  # Server says no data — don't retry
            
            if len(resp.content) < 500:
                print(f"[WARN] WMS response too small ({len(resp.content)}B) for {layer_name} on {date_str}", file=sys.stderr)
                continue
                
            # Write successful image buffer to disk cache
            try:
                with open(cache_path, "wb") as f:
                    f.write(resp.content)
            except Exception as e:
                print(f"[WARN] Failed to write cache for {layer_name}: {e}", file=sys.stderr)
                
            arr = np.frombuffer(resp.content, np.uint8)
            img = cv2.imdecode(arr, cv2.IMREAD_UNCHANGED)
            if img is not None:
                # Ensure grayscale images (like specific satellite bands) are converted to 3-channel BGR
                if img.ndim == 2:
                    img = cv2.cvtColor(img, cv2.COLOR_GRAY2BGR)
                return img
            print(f"[WARN] cv2.imdecode failed for {layer_name} on {date_str}", file=sys.stderr)
            
        except requests.exceptions.Timeout:
            print(f"[WARN] Timeout ({timeout}s) for {layer_name} on {date_str} (attempt {attempt+1}/{max_retries})", file=sys.stderr)
        except Exception as e:
            print(f"[WARN] Fetch error for {layer_name} on {date_str}: {e} (attempt {attempt+1})", file=sys.stderr)
    
    print(f"[ERROR] All {max_retries} attempts failed for {layer_name} on {date_str}", file=sys.stderr)
    return None

def alpha_composite(bg, fg, is_overlay=True, layer_opacity=1.0):
    """Composites foreground over background using exact alpha channel or threshold."""
    if fg is None: return bg
    if bg is None:
        return fg[:, :, :3].copy() if fg.ndim == 3 else cv2.cvtColor(fg, cv2.COLOR_GRAY2BGR)
    
    if fg.shape[:2] != bg.shape[:2]:
        fg = cv2.resize(fg, (bg.shape[1], bg.shape[0]))
    
    bg_f = bg.astype(np.float32)
    
    # Base layer: draw fully opaque
    if not is_overlay:
        return fg[:, :, :3].copy() if fg.ndim == 3 else cv2.cvtColor(fg, cv2.COLOR_GRAY2BGR)
    
    # Overlay with real alpha channel (4-channel PNG from WMS with TRANSPARENT=TRUE)
    if fg.ndim == 3 and fg.shape[2] == 4:
        fg_rgb = fg[:, :, :3].astype(np.float32)
        alpha = (fg[:, :, 3] / 255.0).astype(np.float32)[:, :, np.newaxis] * layer_opacity
        out = alpha * fg_rgb + (1.0 - alpha) * bg_f
        return np.clip(out, 0, 255).astype(np.uint8)
    
    # Overlay without alpha (3-channel): use color-based transparency
    # Dark/black pixels = no data → transparent; colored pixels = real data → show
    fg_rgb = fg[:, :, :3].astype(np.float32)
    brightness = fg_rgb.sum(axis=2)  # R+G+B sum
    # Threshold=10 (not 30) to preserve dim but real data like DayNightBand city lights
    alpha = np.where(brightness > 10, 1.0, 0.0).astype(np.float32)[:, :, np.newaxis] * layer_opacity
    out = alpha * fg_rgb + (1.0 - alpha) * bg_f
    return np.clip(out, 0, 255).astype(np.uint8)

def calculate_activity(img):
    """Calculates percentage of active (non-transparent/non-black) pixels.
    Threshold=3 to catch dim nighttime data (DayNightBand city lights, aurora)."""
    if img is None: return 0.0
    if img.ndim == 2:
        # Grayscale image (no channel dimension)
        active = np.count_nonzero(img > 3)
    elif img.shape[2] == 4:
        active = np.count_nonzero(img[:, :, 3] > 0)
    else:
        gray = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
        active = np.count_nonzero(gray > 3)
    return (active / (WIDTH * HEIGHT)) * 100.0

def draw_hud(frame, date_str, bbox, layers, history, max_days):
    overlay = frame.copy()
    
    # Top HUD
    cv2.rectangle(overlay, (0, 0), (WIDTH, 40), (0, 0, 0), -1)
    
    # Bottom HUD (taller for graph)
    hud_h = max(120, len(layers) * 20 + 20)
    cv2.rectangle(overlay, (0, HEIGHT-hud_h), (WIDTH, HEIGHT), (0, 0, 0), -1)
    cv2.addWeighted(overlay, 0.7, frame, 0.3, 0, frame)
    
    font = cv2.FONT_HERSHEY_SIMPLEX
    cv2.putText(frame, "BATCH N-LAYER WMS RENDER (MP4)", (20, 25), font, 0.6, (50, 150, 255), 2, cv2.LINE_AA)
    cv2.putText(frame, f"DATE: {date_str}", (WIDTH - 200, 25), font, 0.6, (255, 255, 255), 1, cv2.LINE_AA)
    
    lat = (bbox[1] + bbox[3]) / 2
    lon = (bbox[0] + bbox[2]) / 2
    cv2.putText(frame, f"TGT COORD: {lat:.4f}, {lon:.4f}", (WIDTH//2 - 120, 25), font, 0.5, (200, 200, 200), 1, cv2.LINE_AA)

    # Layer List
    for i, layer in enumerate(layers):
        y_pos = HEIGHT - hud_h + 20 + (i * 20)
        cv2.putText(frame, f"L{i+1}: {layer['name']}", (20, y_pos), font, 0.5, (200, 255, 200), 1, cv2.LINE_AA)

    # Draw Activity Graph
    if history:
        graph_w = 400
        graph_h = 80
        gx = WIDTH - graph_w - 20
        gy = HEIGHT - graph_h - 20
        
        cv2.rectangle(frame, (gx, gy), (gx + graph_w, gy + graph_h), (50, 50, 50), 1)
        cv2.putText(frame, "Top Layer Activity %", (gx, gy - 8), font, 0.4, (200, 200, 200), 1, cv2.LINE_AA)
        
        max_val = max(1.0, max(history) * 1.2)
        points = []
        for i, val in enumerate(history):
            x = gx + int((i / max(1, max_days - 1)) * graph_w)
            y = gy + graph_h - int((val / max_val) * graph_h)
            points.append((x, y))
            
        for i in range(1, len(points)):
            cv2.line(frame, points[i-1], points[i], (0, 255, 255), 2, cv2.LINE_AA)
            
        cv2.putText(frame, f"{history[-1]:.2f}%", (gx + graph_w - 60, gy + 20), font, 0.5, (0, 255, 255), 1, cv2.LINE_AA)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--job_id", required=True)
    parser.add_argument("--bbox", required=True)
    parser.add_argument("--start", required=True)
    parser.add_argument("--end", required=True)
    parser.add_argument("--fps", type=int, default=30)
    parser.add_argument("--frames", type=int, default=100)
    parser.add_argument("--out", required=True)
    parser.add_argument("--layers", required=True)
    parser.add_argument("--freq", default="1d", help="Time step frequency (e.g. 1d, 1h, 10m)")
    parser.add_argument("--track", default="", help="Satellite name to track (e.g. TERRA, AQUA, ISS)")
    args = parser.parse_args()
    
    bbox = [float(x.strip()) for x in args.bbox.split(",")]
    start_date = datetime.datetime.strptime(args.start, "%Y-%m-%d")
    end_date = datetime.datetime.strptime(args.end, "%Y-%m-%d")
    # For sub-daily requests, we need an exact 23:59:59 end boundary to include the last day's images
    end_date = end_date.replace(hour=23, minute=59, second=59)
    
    freq = args.freq.lower()
    
    # Parse temporal resolution into a proper timedelta
    freq_map = {'1d': datetime.timedelta(days=1), '1h': datetime.timedelta(hours=1),
                '10m': datetime.timedelta(minutes=10), '1m': datetime.timedelta(minutes=1)}
    freq_delta = freq_map.get(freq, datetime.timedelta(days=1))
    
    # Calculate how many data points exist at the chosen frequency (fence-post: +1 for inclusive end)
    time_range = end_date - start_date
    data_steps = max(1, int(time_range / freq_delta) + 1)
    
    # Cap steps at user-requested frame count, but calculate frames_per_step 
    # to ensure the final video length matches the user's request.
    total_steps = min(args.frames, data_steps)
    delta = freq_delta if total_steps == data_steps else time_range / max(1, total_steps)
    layers = json.loads(args.layers)
    if not layers:
        sys.exit("No WMS layers provided.")
        
    out_dir = os.path.dirname(args.out)
    if out_dir and not os.path.exists(out_dir):
        os.makedirs(out_dir)

    # Try codecs in order of stability on Windows (avc1 needs openh264 dll and crashes often)
    out = None
    for codec in ['mp4v', 'avc1', 'XVID']:
        fourcc = cv2.VideoWriter_fourcc(*codec)
        out = cv2.VideoWriter(args.out, fourcc, args.fps, (WIDTH, HEIGHT))
        if out.isOpened():
            break
        out.release()
    
    if out is None or not out.isOpened():
        sys.exit("Failed to initialize video writer with any codec.")

    # Calculate how many frames to write per data step so the total equals args.frames
    frames_per_step = max(1, args.frames // max(1, total_steps))
    
    # ── Orbital Tracking Setup ──
    tracking_mode = False
    tle_line1, tle_line2 = None, None
    sat_name = args.track.upper().replace(" ", "_") if args.track else ""
    if sat_name:
        try:
            from orbital_tracker import fetch_tle, propagate, compute_tracking_bbox, SATELLITE_CATALOG
            norad_id = SATELLITE_CATALOG.get(sat_name)
            if norad_id:
                name_str, tle_line1, tle_line2 = fetch_tle(norad_id)
                if tle_line1 and tle_line2:
                    tracking_mode = True
                    print(f"ORBITAL TRACKING ENABLED: {sat_name} (NORAD {norad_id})", file=sys.stderr)
                else:
                    print(f"[WARN] Failed to fetch TLE for {sat_name}", file=sys.stderr)
            else:
                print(f"[WARN] Unknown satellite: {sat_name}. Available: {list(SATELLITE_CATALOG.keys())}", file=sys.stderr)
        except ImportError as e:
            print(f"[WARN] orbital_tracker module not available: {e}", file=sys.stderr)
    
    activity_history = []
    last_good_frame = None  # Frame persistence: reuse last good frame when no data
    last_good_date = None
    
    current_date = start_date
    step = 0
    while current_date <= end_date and step < total_steps:
        # If it's a daily step, request YYYY-MM-DD. If sub-daily, request ISO 8601 (YYYY-MM-DDTHH:MM:SSZ)
        if delta.total_seconds() >= 86400:
            date_str = current_date.strftime("%Y-%m-%d")
        else:
            date_str = current_date.strftime("%Y-%m-%dT%H:%M:%SZ")
            
        frame = None
        
        # If tracking, dynamically compute BBOX from satellite position
        sat_telemetry = None
        active_bbox = bbox  # default static bbox
        if tracking_mode and tle_line1 and tle_line2:
            from orbital_tracker import propagate, compute_tracking_bbox
            sat_telemetry = propagate(tle_line1, tle_line2, current_date)
            if sat_telemetry:
                active_bbox = compute_tracking_bbox(sat_telemetry['lat'], sat_telemetry['lon'])
        
        futures_map = {}
        with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, len(layers))) as executor:
            for idx, layer in enumerate(layers):
                is_base = (idx == 0)
                future = executor.submit(fetch_wms_image, layer['url'], layer['name'], active_bbox, date_str, is_base)
                futures_map[future] = idx
                
        layer_images = [None] * len(layers)
        for future in concurrent.futures.as_completed(futures_map):
            idx = futures_map[future]
            layer_images[idx] = future.result()
            
        for idx, img in enumerate(layer_images):
            if img is not None:
                # Use specified opacity (default 1.0)
                opacity = layers[idx].get('opacity', 1.0)
                frame = alpha_composite(frame, img, is_overlay=(idx > 0), layer_opacity=opacity)
        
        # Calculate activity metric for top layer
        top_img = layer_images[-1] if layer_images else None
        metric = calculate_activity(top_img)
        activity_history.append(metric)
        
        is_gap = False
        
        if frame is not None:
            # Check if frame has actual content (not just black pixels)
            gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY) if frame.ndim == 3 else frame
            if np.mean(gray) > 3:  # has visible content
                last_good_frame = frame.copy()
                last_good_date = date_str
            else:
                # Frame is nearly all black — use last good frame with gap indicator
                if last_good_frame is not None:
                    frame = last_good_frame.copy()
                    is_gap = True
        elif last_good_frame is not None:
            # No data at all — reuse last good frame with gap indicator
            frame = last_good_frame.copy()
            is_gap = True
        else:
            # No data and no previous frame — show "no data" text
            frame = np.zeros((HEIGHT, WIDTH, 3), dtype=np.uint8)
            cv2.putText(frame, f"AWAITING DATA: {date_str}", (WIDTH//2 - 200, HEIGHT//2),
                       cv2.FONT_HERSHEY_SIMPLEX, 0.9, (0, 100, 255), 2)
        
        # If using persisted frame, show transparent DATA GAP banner
        if is_gap:
            overlay = frame.copy()
            cv2.rectangle(overlay, (WIDTH - 380, 48), (WIDTH, 80), (0, 0, 180), -1)
            cv2.addWeighted(overlay, 0.6, frame, 0.4, 0, frame)
            gap_text = f"DATA GAP - Showing {last_good_date}" if last_good_date else "DATA GAP"
            cv2.putText(frame, gap_text, (WIDTH - 375, 70),
                       cv2.FONT_HERSHEY_SIMPLEX, 0.4, (255, 200, 200), 1, cv2.LINE_AA)
        
        frame = frame.astype(np.uint8)
        draw_hud(frame, date_str, active_bbox, layers, activity_history, total_steps)
        
        # ── Aviation-Style Telemetry HUD (only when tracking) ──
        if tracking_mode and sat_telemetry:
            font = cv2.FONT_HERSHEY_SIMPLEX
            # Semi-transparent panel on the right
            overlay = frame.copy()
            cv2.rectangle(overlay, (WIDTH - 320, 45), (WIDTH, 170), (0, 0, 0), -1)
            cv2.addWeighted(overlay, 0.75, frame, 0.25, 0, frame)
            
            cv2.putText(frame, f"TRACKING: {sat_name}", (WIDTH - 310, 65), font, 0.5, (0, 255, 100), 1, cv2.LINE_AA)
            cv2.putText(frame, f"ALT: {sat_telemetry['alt_km']:.1f} km", (WIDTH - 310, 85), font, 0.4, (0, 200, 255), 1, cv2.LINE_AA)
            cv2.putText(frame, f"VEL: {sat_telemetry['velocity_km_s']:.3f} km/s", (WIDTH - 310, 103), font, 0.4, (0, 200, 255), 1, cv2.LINE_AA)
            cv2.putText(frame, f"LAT: {sat_telemetry['lat']:.4f}", (WIDTH - 310, 121), font, 0.4, (200, 200, 200), 1, cv2.LINE_AA)
            cv2.putText(frame, f"LON: {sat_telemetry['lon']:.4f}", (WIDTH - 310, 139), font, 0.4, (200, 200, 200), 1, cv2.LINE_AA)
            cv2.putText(frame, f"INC: {sat_telemetry['inclination_deg']:.2f} deg", (WIDTH - 310, 157), font, 0.4, (180, 180, 180), 1, cv2.LINE_AA)
        
        # Write frames_per_step frames to pad the video to the requested length
        for _ in range(frames_per_step):
            out.write(frame)
        

        
        current_date += delta
        step += 1
        
        # ── Progress Reporting (consumed by Go pipeline via stdout) ──
        current_frames = min(args.frames, step * frames_per_step)
        print(json.dumps({"progress": current_frames, "total": args.frames}), flush=True)

    out.release()
    
    # ── FFmpeg Re-encode: OpenCV writes mp4v (MPEG-4 Part 2) which browsers CANNOT play.
    # Browsers need H.264. Re-encode to H.264 with web-optimized settings.
    temp_path = args.out.replace(".mp4", "_raw.mp4")
    os.rename(args.out, temp_path)
    try:
        ffmpeg_cmd = [
            "ffmpeg", "-y", "-i", temp_path,
            "-c:v", "libx264", "-preset", "fast", "-crf", "23",
            "-pix_fmt", "yuv420p",  # Required for browser compatibility
            "-movflags", "+faststart",  # Enables progressive download (play before fully loaded)
            args.out
        ]
        result = subprocess.run(ffmpeg_cmd, capture_output=True, text=True, timeout=120)
        if result.returncode != 0:
            print(f"[WARN] FFmpeg re-encode failed: {result.stderr}", file=sys.stderr)
            # Fallback: use the raw OpenCV file
            os.rename(temp_path, args.out)
        else:
            os.remove(temp_path)
            print("FFmpeg re-encode to H.264 successful.", file=sys.stderr)
    except FileNotFoundError:
        print("[WARN] FFmpeg not found, using raw OpenCV output (may not play in browsers)", file=sys.stderr)
        os.rename(temp_path, args.out)
    except subprocess.TimeoutExpired:
        print("[WARN] FFmpeg timed out, using raw OpenCV output", file=sys.stderr)
        if os.path.exists(temp_path):
            os.rename(temp_path, args.out)

    # Save metrics to JSON for the Go backend to consume
    metrics_path = args.out.replace(".mp4", "_metrics.json")
    with open(metrics_path, "w") as f:
        json.dump(activity_history, f)
        
    print("Video generation completed successfully.")

if __name__ == "__main__":
    main()
