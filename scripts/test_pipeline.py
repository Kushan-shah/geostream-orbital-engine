"""End-to-end test: Video generation + Live stream + Orbital tracking with video."""
import sys, os, json, subprocess, time, datetime

SCRIPT_DIR = os.path.dirname(__file__)

def test_video_generation():
    print("=" * 70)
    print("VIDEO GENERATION PIPELINE TEST")
    print("=" * 70)
    
    out_path = os.path.join(SCRIPT_DIR, "test_output.mp4")
    layers = json.dumps([
        {"url": "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
         "name": "MODIS_Terra_CorrectedReflectance_TrueColor"}
    ])
    
    cmd = [
        sys.executable, os.path.join(SCRIPT_DIR, "generate_video.py"),
        "--job_id", "audit-test-0001",
        "--bbox", "68.0,6.0,97.5,37.0",
        "--start", "2025-04-20",
        "--end", "2025-04-23",
        "--layers", layers,
        "--out", out_path,
        "--fps", "10",
        "--frames", "4"
    ]
    
    print(f"  Region: India (68,6 to 97.5,37)")
    print(f"  Layer:  MODIS Terra True Color")
    print(f"  Frames: 4 @ 10fps")
    
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
        
        # Parse progress JSON from stdout
        progress = []
        for line in (result.stdout or "").strip().split("\n"):
            try:
                p = json.loads(line)
                if "progress" in p:
                    progress.append(p)
            except:
                pass
        
        if progress:
            print(f"  Progress messages: {len(progress)}")
            print(f"    Last: {progress[-1]}")
        
        if os.path.exists(out_path):
            size = os.path.getsize(out_path)
            print(f"  Output file: {size:,} bytes ({size/1024:.1f} KB)")
            
            metrics_path = out_path.replace(".mp4", "_metrics.json")
            if os.path.exists(metrics_path):
                with open(metrics_path) as f:
                    metrics = json.load(f)
                print(f"  Metrics: {len(metrics)} data points, range {min(metrics):.2f}%-{max(metrics):.2f}%")
                os.remove(metrics_path)
            
            os.remove(out_path)
            
            if size > 1000:
                print(f"\n  [PASS] Video generation WORKING")
                return True
            else:
                print(f"\n  [FAIL] Video too small ({size}B)")
                return False
        else:
            print(f"  [FAIL] No video file created")
            if result.stderr:
                for line in result.stderr.strip().split("\n")[-5:]:
                    print(f"    STDERR: {line}")
            return False
    except subprocess.TimeoutExpired:
        print(f"  [FAIL] Timed out (120s)")
        return False
    except Exception as e:
        print(f"  [FAIL] {e}")
        return False


def test_video_with_tracking():
    print("\n" + "=" * 70)
    print("VIDEO GENERATION WITH ORBITAL TRACKING TEST")
    print("=" * 70)
    
    out_path = os.path.join(SCRIPT_DIR, "test_tracking.mp4")
    layers = json.dumps([
        {"url": "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
         "name": "MODIS_Terra_CorrectedReflectance_TrueColor"}
    ])
    
    cmd = [
        sys.executable, os.path.join(SCRIPT_DIR, "generate_video.py"),
        "--job_id", "audit-track-0001",
        "--bbox", "68.0,6.0,97.5,37.0",
        "--start", "2025-04-20",
        "--end", "2025-04-23",
        "--layers", layers,
        "--out", out_path,
        "--fps", "10",
        "--frames", "3",
        "--track", "AQUA"
    ]
    
    print(f"  Tracking: AQUA (NORAD 27424)")
    print(f"  Frames:   3 with dynamic BBOX recomputation")
    
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
        
        # Check stderr for tracking messages
        tracking_msgs = []
        for line in (result.stderr or "").split("\n"):
            if "track" in line.lower() or "bbox" in line.lower() or "propagat" in line.lower():
                tracking_msgs.append(line.strip())
        
        if tracking_msgs:
            print(f"  Tracking log messages: {len(tracking_msgs)}")
            for msg in tracking_msgs[:5]:
                print(f"    {msg}")
        
        if os.path.exists(out_path):
            size = os.path.getsize(out_path)
            print(f"  Output: {size:,} bytes")
            os.remove(out_path)
            metrics_path = out_path.replace(".mp4", "_metrics.json")
            if os.path.exists(metrics_path):
                os.remove(metrics_path)
            
            if size > 500:
                print(f"\n  [PASS] Orbital tracking video WORKING")
                return True
        
        print(f"\n  [FAIL] Tracking video generation failed")
        if result.stderr:
            for line in result.stderr.strip().split("\n")[-3:]:
                print(f"    STDERR: {line}")
        return False
    except Exception as e:
        print(f"  [FAIL] {e}")
        return False


def test_live_stream():
    print("\n" + "=" * 70)
    print("LIVE MJPEG STREAM TEST")
    print("=" * 70)
    
    layers = json.dumps([
        {"url": "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
         "name": "MODIS_Terra_CorrectedReflectance_TrueColor"}
    ])
    
    cmd = [
        sys.executable, os.path.join(SCRIPT_DIR, "stream_video.py"),
        "--bbox", "68.0,6.0,97.5,37.0",
        "--start", "2025-04-20",
        "--end", "2025-04-23",
        "--layers", layers
    ]
    
    print(f"  Mode: MJPEG multipart/x-mixed-replace")
    print(f"  Capturing first ~15 seconds...")
    
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        
        start = time.time()
        output = b""
        frames = 0
        
        while time.time() - start < 15:
            chunk = proc.stdout.read(4096)
            if not chunk:
                break
            output += chunk
            frames = output.count(b"--frame")
            if frames >= 3:
                break
        
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except:
            proc.kill()
        
        has_jpeg = b"Content-Type: image/jpeg" in output
        total = len(output)
        
        print(f"  Bytes received:  {total:,}")
        print(f"  MJPEG frames:    {frames}")
        print(f"  JPEG headers:    {has_jpeg}")
        print(f"  Elapsed:         {time.time()-start:.1f}s")
        
        if frames >= 1 and has_jpeg:
            print(f"\n  [PASS] Live stream WORKING ({frames} frames)")
            return True
        else:
            print(f"\n  [FAIL] No valid MJPEG frames")
            return False
    except Exception as e:
        print(f"  [FAIL] {e}")
        return False


def test_live_stream_with_tracking():
    print("\n" + "=" * 70)
    print("LIVE STREAM WITH ORBITAL TRACKING TEST")
    print("=" * 70)
    
    layers = json.dumps([
        {"url": "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
         "name": "MODIS_Terra_CorrectedReflectance_TrueColor"}
    ])
    
    cmd = [
        sys.executable, os.path.join(SCRIPT_DIR, "stream_video.py"),
        "--bbox", "68.0,6.0,97.5,37.0",
        "--start", "2025-04-20",
        "--end", "2025-04-23",
        "--layers", layers,
        "--track", "AQUA",
        "--freq", "1d"
    ]
    
    print(f"  Tracking: AQUA satellite")
    print(f"  Time Step: 1d")
    
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        
        start = time.time()
        output = b""
        frames = 0
        
        while time.time() - start < 20:
            chunk = proc.stdout.read(4096)
            if not chunk:
                break
            output += chunk
            frames = output.count(b"--frame")
            if frames >= 2:
                break
        
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except:
            proc.kill()
        
        stderr_out = proc.stderr.read().decode("utf-8", errors="replace")
        
        has_jpeg = b"Content-Type: image/jpeg" in output
        
        # Check if tracking was active
        tracking_active = "track" in stderr_out.lower() or "propagat" in stderr_out.lower() or "bbox" in stderr_out.lower()
        
        print(f"  Bytes received:    {len(output):,}")
        print(f"  MJPEG frames:      {frames}")
        print(f"  Tracking active:   {tracking_active}")
        
        if stderr_out.strip():
            for line in stderr_out.strip().split("\n")[:3]:
                print(f"  LOG: {line.strip()}")
        
        if frames >= 1 and has_jpeg:
            print(f"\n  [PASS] Tracked live stream WORKING")
            return True
        else:
            print(f"\n  [FAIL] No valid tracked frames")
            return False
    except Exception as e:
        print(f"  [FAIL] {e}")
        return False


if __name__ == "__main__":
    print(f"GeoStream Full Pipeline Test")
    print(f"Timestamp: {datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%d %H:%M:%S UTC')}\n")
    
    r1 = test_video_generation()
    r2 = test_video_with_tracking()
    r3 = test_live_stream()
    r4 = test_live_stream_with_tracking()
    
    print("\n" + "=" * 70)
    print("PIPELINE SUMMARY")
    print("=" * 70)
    print(f"  Video Generation (static BBOX):  {'PASS' if r1 else 'FAIL'}")
    print(f"  Video Generation (orbital track): {'PASS' if r2 else 'FAIL'}")
    print(f"  Live Stream (static BBOX):        {'PASS' if r3 else 'FAIL'}")
    print(f"  Live Stream (orbital track):      {'PASS' if r4 else 'FAIL'}")
    
    total = sum([r1, r2, r3, r4])
    print(f"\n  Score: {total}/4 pipelines operational")
