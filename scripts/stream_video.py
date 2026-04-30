# Copyright 2026 Kushan J
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

WIDTH = 1280
HEIGHT = 720

# Create cache directory for WMS tiles
CACHE_DIR = "cache/wms"
if not os.path.exists(CACHE_DIR):
    os.makedirs(CACHE_DIR, exist_ok=True)

def fetch_wms_image(url_template, layer_name, bbox, date_str, is_base=False):
    """Fetch a WMS image with disk caching. Base layers = JPEG (opaque), overlays = PNG (with alpha).
    For non-GIBS servers (GOES, NEXRAD, etc.), skip the TIME param — they serve live/latest data."""
    base_url = url_template.split("?")[0] if "?" in url_template else url_template
    fmt = "image/jpeg" if is_base else "image/png"
    is_gibs = "gibs.earthdata.nasa.gov" in url_template
    time_param = f"&TIME={date_str}" if (is_gibs and "BlueMarble" not in layer_name) else ""
    url = f"{base_url}?SERVICE=WMS&REQUEST=GetMap&VERSION=1.1.1&LAYERS={layer_name}&SRS=EPSG:4326&BBOX={bbox[0]},{bbox[1]},{bbox[2]},{bbox[3]}&WIDTH={WIDTH}&HEIGHT={HEIGHT}&FORMAT={fmt}{time_param}&TRANSPARENT={'FALSE' if is_base else 'TRUE'}"
    
    # Disk cache check
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
        except Exception:
            pass

    try:
        resp = requests.get(url, headers={'User-Agent': 'GeoStream-CV/1.0'}, timeout=15)
        if resp.status_code == 200 and len(resp.content) > 500:
            # Write to cache
            try:
                with open(cache_path, "wb") as f:
                    f.write(resp.content)
            except Exception:
                pass
            arr = np.frombuffer(resp.content, np.uint8)
            img = cv2.imdecode(arr, cv2.IMREAD_UNCHANGED)
            if img is not None and img.ndim == 2:
                img = cv2.cvtColor(img, cv2.COLOR_GRAY2BGR)
            return img
    except Exception:
        pass
    return None

def alpha_composite(bg, fg, is_overlay=True, layer_opacity=1.0):
    """Composites foreground over background using exact alpha channel or threshold."""
    if fg is None: return bg
    if bg is None:
        if fg.ndim == 3 and fg.shape[2] == 4:
            return fg[:, :, :3].copy()
        return fg[:, :, :3].copy() if fg.ndim == 3 else cv2.cvtColor(fg, cv2.COLOR_GRAY2BGR)
    
    if fg.shape[:2] != bg.shape[:2]:
        fg = cv2.resize(fg, (bg.shape[1], bg.shape[0]))
    
    bg_f = bg.astype(np.float32)
    
    if not is_overlay:
        return fg[:, :, :3].copy() if fg.ndim == 3 else cv2.cvtColor(fg, cv2.COLOR_GRAY2BGR)
    
    if fg.ndim == 3 and fg.shape[2] == 4:
        fg_rgb = fg[:, :, :3].astype(np.float32)
        alpha = (fg[:, :, 3] / 255.0).astype(np.float32)[:, :, np.newaxis] * layer_opacity
        out = alpha * fg_rgb + (1.0 - alpha) * bg_f
        return np.clip(out, 0, 255).astype(np.uint8)
    
    fg_rgb = fg[:, :, :3].astype(np.float32)
    brightness = fg_rgb.sum(axis=2)
    # Threshold=10 (not 30) to preserve dim but real data like DayNightBand city lights
    alpha = np.where(brightness > 10, 1.0, 0.0).astype(np.float32)[:, :, np.newaxis] * layer_opacity
    out = alpha * fg_rgb + (1.0 - alpha) * bg_f
    return np.clip(out, 0, 255).astype(np.uint8)

def calculate_activity(img):
    """Calculates percentage of active (non-transparent/non-black) pixels.
    Threshold=3 to catch dim nighttime data (DayNightBand city lights, aurora)."""
    if img is None: return 0.0
    if img.ndim == 2:
        active = np.count_nonzero(img > 3)
    elif img.shape[2] == 4:
        active = np.count_nonzero(img[:, :, 3] > 0)
    else:
        gray = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
        active = np.count_nonzero(gray > 3)
    return (active / (WIDTH * HEIGHT)) * 100.0

def detect_data_gap(frame):
    """Detect percentage of black (no-data) pixels in the imagery area (excluding HUD)."""
    # Only analyze the middle portion of the frame (exclude top/bottom HUD bars)
    roi = frame[50:HEIGHT-130, :, :]
    if roi.size == 0:
        return 0.0
    gray = cv2.cvtColor(roi, cv2.COLOR_BGR2GRAY)
    black_pixels = np.count_nonzero(gray < 5)
    total_pixels = gray.shape[0] * gray.shape[1]
    return (black_pixels / total_pixels) * 100.0

def draw_hud(frame, frame_idx, date_str, bbox, layers, history, refresh_count):
    overlay = frame.copy()
    
    cv2.rectangle(overlay, (0, 0), (WIDTH, 44), (0, 0, 0), -1)
    
    hud_h = max(120, len(layers) * 20 + 20)
    cv2.rectangle(overlay, (0, HEIGHT-hud_h), (WIDTH, HEIGHT), (0, 0, 0), -1)
    cv2.addWeighted(overlay, 0.7, frame, 0.3, 0, frame)
    
    font = cv2.FONT_HERSHEY_SIMPLEX
    
    # Title with LIVE badge
    cv2.putText(frame, "LIVE", (20, 28), font, 0.7, (0, 0, 255), 2, cv2.LINE_AA)
    cv2.putText(frame, "NEAR REAL-TIME WMS MONITOR", (80, 28), font, 0.55, (50, 150, 255), 2, cv2.LINE_AA)
    
    # Current timestamp (actual system clock)
    now_str = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    cv2.putText(frame, now_str, (WIDTH - 280, 28), font, 0.5, (255, 255, 255), 1, cv2.LINE_AA)
    
    # Satellite data date
    lat = (bbox[1] + bbox[3]) / 2
    lon = (bbox[0] + bbox[2]) / 2
    cv2.putText(frame, f"SAT DATA: {date_str}  |  COORD: {lat:.2f}, {lon:.2f}  |  REFRESH #{refresh_count}", (20, HEIGHT - hud_h + 18), font, 0.4, (180, 180, 180), 1, cv2.LINE_AA)

    # Layer List
    for i, layer in enumerate(layers):
        y_pos = HEIGHT - hud_h + 38 + (i * 20)
        cv2.putText(frame, f"L{i+1}: {layer['name']}", (20, y_pos), font, 0.45, (200, 255, 200), 1, cv2.LINE_AA)

    # Activity Graph
    if history:
        graph_w = 400
        graph_h = 80
        gx = WIDTH - graph_w - 20
        gy = HEIGHT - graph_h - 20
        
        cv2.rectangle(frame, (gx, gy), (gx + graph_w, gy + graph_h), (50, 50, 50), 1)
        cv2.putText(frame, "Top Layer Activity % (per refresh)", (gx, gy - 8), font, 0.35, (200, 200, 200), 1, cv2.LINE_AA)
        
        max_val = max(1.0, max(history) * 1.2)
        max_pts = 60  # show last 60 data points
        visible = history[-max_pts:]
        points = []
        for i, val in enumerate(visible):
            x = gx + int((i / max(1, len(visible) - 1)) * graph_w)
            y = gy + graph_h - int((val / max_val) * graph_h)
            points.append((x, y))
        for i in range(1, len(points)):
            cv2.line(frame, points[i-1], points[i], (0, 255, 255), 2, cv2.LINE_AA)
        cv2.putText(frame, f"{history[-1]:.2f}%", (gx + graph_w - 65, gy + 18), font, 0.45, (0, 255, 255), 1, cv2.LINE_AA)

    # Blinking red dot
    if (frame_idx % 2) == 0:
        cv2.circle(frame, (55, 25), 5, (0, 0, 255), -1)

    # Data gap indicator — only shown when a true color/reflectance base layer is present.
    # Overlay-only layers (fire, thermal, aerosol) are expected to be mostly black.
    base_layer_keywords = ['TrueColor', 'Reflectance', 'GeoColor', 'Band3', 'Visible',
                           'Sea_Surface', 'NDVI', 'EVI', 'Snow_Cover', 'DayNightBand',
                           'GEBCO', 's2cloudless', 'goes_west', 'nexrad']
    has_base_layer = any(
        any(kw.lower() in layer.get('name', '').lower() for kw in base_layer_keywords)
        for layer in layers
    )
    if has_base_layer:
        gap_pct = detect_data_gap(frame)
        if gap_pct > 15.0:
            gap_text = f"COVERAGE GAP: {gap_pct:.0f}% — satellite swath did not cover this area on this date"
            text_w = cv2.getTextSize(gap_text, font, 0.4, 1)[0][0]
            bar_y = 48
            cv2.rectangle(frame, (0, bar_y), (text_w + 40, bar_y + 22), (0, 40, 80), -1)
            cv2.putText(frame, gap_text, (20, bar_y + 15), font, 0.4, (0, 180, 255), 1, cv2.LINE_AA)

def yield_frame(frame):
    ret, buffer = cv2.imencode('.jpg', frame, [int(cv2.IMWRITE_JPEG_QUALITY), 85])
    if ret:
        sys.stdout.buffer.write(b'--frame\r\n')
        sys.stdout.buffer.write(b'Content-Type: image/jpeg\r\n\r\n')
        sys.stdout.buffer.write(buffer.tobytes())
        sys.stdout.buffer.write(b'\r\n')
        sys.stdout.flush()

def find_latest_date_with_data(layers, bbox, base_date_str, max_lookback=7):
    """Try recent dates (starting from base_date) to find the most recent one with actual data.
    Uses a base/imagery layer for testing — NOT sparse overlays like fire detection
    (which are mostly black even when data exists)."""
    # Find a suitable test layer (prefer true color/reflectance, avoid sparse overlays)
    base_keywords = ['TrueColor', 'Reflectance', 'GeoColor', 'Visible', 'Band3',
                     'NDVI', 'EVI', 'Snow_Cover', 'Sea_Surface', 'DayNightBand',
                     'GEBCO', 's2cloudless', 'goes_west', 'nexrad', 'Infrared',
                     'Cloud_Top', 'Water_Vapor', 'Air_Mass', 'Dust', 'Soil_Moisture']
    test_layer = layers[0]  # default
    for layer in layers:
        if any(kw.lower() in layer.get('name', '').lower() for kw in base_keywords):
            test_layer = layer
            break

    try:
        base_date = datetime.datetime.strptime(base_date_str, "%Y-%m-%d").replace(tzinfo=datetime.timezone.utc)
    except:
        base_date = datetime.datetime.now(datetime.timezone.utc)

    for days_ago in range(0, max_lookback):
        date = (base_date - datetime.timedelta(days=days_ago)).strftime("%Y-%m-%d")
        img = fetch_wms_image(test_layer['url'], test_layer['name'], bbox, date, is_base=True)
        if img is not None:
            gray = cv2.cvtColor(img[:, :, :3] if img.shape[2] >= 3 else img, cv2.COLOR_BGR2GRAY) if img.ndim == 3 else img
            if np.mean(gray) > 3:  # lowered from 5→3 for DayNightBand support
                return date
    # fallback: yesterday relative to base_date
    return (base_date - datetime.timedelta(days=1)).strftime("%Y-%m-%d")

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--bbox", required=True)
    parser.add_argument("--start", required=True)
    parser.add_argument("--end", required=True)
    parser.add_argument("--layers", required=True)
    parser.add_argument("--freq", default="1d", help="Time step frequency (e.g. 1d, 1h, 10m)")
    parser.add_argument("--track", default="", help="Satellite name to track (e.g. TERRA, AQUA, ISS)")
    args = parser.parse_args()
    
    bbox = [float(x.strip()) for x in args.bbox.split(",")]
    
    layers = json.loads(args.layers)
    if not layers:
        sys.exit("No WMS layers provided.")
    
    # Loading frame
    loading_frame = np.zeros((HEIGHT, WIDTH, 3), dtype=np.uint8)
    cv2.putText(loading_frame, "CONNECTING TO NASA WMS SERVERS...", (WIDTH//2 - 260, HEIGHT//2 - 20), cv2.FONT_HERSHEY_SIMPLEX, 0.8, (0, 255, 0), 2, cv2.LINE_AA)
    cv2.putText(loading_frame, "Searching for latest available imagery...", (WIDTH//2 - 220, HEIGHT//2 + 20), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 200, 0), 1, cv2.LINE_AA)
    yield_frame(loading_frame)

    frame_idx = 0
    activity_history = []
    refresh_count = 0
    latest_date = None  # Will be computed after first orbital position
    
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
        except ImportError:
            pass
    
    # REAL-TIME MONITORING LOOP
    # Continuously re-fetches data like a live dashboard.
    # Each cycle: compute satellite position, fetch all layers at that position,
    # composite, display for ~2 seconds, then refresh.
    # This runs until the client disconnects (closes the stream modal).
    while True:
        refresh_count += 1
        
        # ── Step 1: Compute current satellite position FIRST ──
        # This must happen BEFORE WMS fetch so the camera follows the satellite.
        sat_telemetry = None
        active_bbox = bbox  # default: user-specified static bbox
        if tracking_mode and tle_line1 and tle_line2:
            from orbital_tracker import propagate, compute_tracking_bbox
            now_utc = datetime.datetime.now(datetime.timezone.utc).replace(tzinfo=None)
            sat_telemetry = propagate(tle_line1, tle_line2, now_utc)
            if sat_telemetry:
                active_bbox = compute_tracking_bbox(sat_telemetry['lat'], sat_telemetry['lon'])
                print(f"TRACKING {sat_name}: lat={sat_telemetry['lat']:.2f} lon={sat_telemetry['lon']:.2f} alt={sat_telemetry['alt_km']:.1f}km bbox={active_bbox}", file=sys.stderr)
        
        # ── Step 2: Find latest date with data (using active_bbox) ──
        if latest_date is None or refresh_count % 10 == 0:
            latest_date = find_latest_date_with_data(layers, active_bbox, args.end)
        
        frame = None
        
        # ── Step 3: Fetch all WMS layers at the satellite's current position ──
        futures_map = {}
        with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, len(layers))) as executor:
            for idx, layer in enumerate(layers):
                is_base = (idx == 0)
                future = executor.submit(fetch_wms_image, layer['url'], layer['name'], active_bbox, latest_date, is_base)
                futures_map[future] = idx
                
        layer_images = [None] * len(layers)
        for future in concurrent.futures.as_completed(futures_map):
            idx = futures_map[future]
            layer_images[idx] = future.result()
            
        for idx, img in enumerate(layer_images):
            if img is not None:
                opacity = layers[idx].get('opacity', 1.0)
                frame = alpha_composite(frame, img, is_overlay=(idx > 0), layer_opacity=opacity)
        
        # Activity metric
        top_img = layer_images[-1] if layer_images else None
        metric = calculate_activity(top_img)
        activity_history.append(metric)

        if frame is None:
            frame = np.zeros((HEIGHT, WIDTH, 3), dtype=np.uint8)
            cv2.putText(frame, f"AWAITING DATA: {latest_date}", (WIDTH//2 - 180, HEIGHT//2), cv2.FONT_HERSHEY_SIMPLEX, 0.9, (0, 100, 255), 2)
        
        frame = frame.astype(np.uint8)
        
        # Show frame for ~2 seconds (20 frames at 0.1s), continuously updating the clock
        for i in range(20):
            display = frame.copy()
            draw_hud(display, frame_idx, latest_date, active_bbox, layers, activity_history, refresh_count)
            
            # ── Aviation Telemetry HUD (tracking mode only) ──
            if tracking_mode and sat_telemetry:
                font = cv2.FONT_HERSHEY_SIMPLEX
                overlay = display.copy()
                cv2.rectangle(overlay, (WIDTH - 320, 45), (WIDTH, 170), (0, 0, 0), -1)
                cv2.addWeighted(overlay, 0.75, display, 0.25, 0, display)
                cv2.putText(display, f"TRACKING: {sat_name}", (WIDTH - 310, 65), font, 0.5, (0, 255, 100), 1, cv2.LINE_AA)
                cv2.putText(display, f"ALT: {sat_telemetry['alt_km']:.1f} km", (WIDTH - 310, 85), font, 0.4, (0, 200, 255), 1, cv2.LINE_AA)
                cv2.putText(display, f"VEL: {sat_telemetry['velocity_km_s']:.3f} km/s", (WIDTH - 310, 103), font, 0.4, (0, 200, 255), 1, cv2.LINE_AA)
                cv2.putText(display, f"LAT: {sat_telemetry['lat']:.4f}", (WIDTH - 310, 121), font, 0.4, (200, 200, 200), 1, cv2.LINE_AA)
                cv2.putText(display, f"LON: {sat_telemetry['lon']:.4f}", (WIDTH - 310, 139), font, 0.4, (200, 200, 200), 1, cv2.LINE_AA)
                cv2.putText(display, f"INC: {sat_telemetry['inclination_deg']:.2f} deg", (WIDTH - 310, 157), font, 0.4, (180, 180, 180), 1, cv2.LINE_AA)
            
            yield_frame(display)
            frame_idx += 1
            time.sleep(0.1)

if __name__ == "__main__":
    if sys.platform == "win32":
        import os, msvcrt
        msvcrt.setmode(sys.stdout.fileno(), os.O_BINARY)
    main()
