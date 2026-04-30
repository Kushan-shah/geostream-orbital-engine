"""Test all 15 orbital satellites — TLE fetch + SGP4 propagation + BBOX generation."""
import sys, os, datetime
sys.path.insert(0, os.path.dirname(__file__))
from orbital_tracker import fetch_tle, propagate, compute_tracking_bbox, SATELLITE_CATALOG

print("=" * 80)
print("ORBITAL SATELLITE TRACKING TEST — All 15 Satellites")
print("=" * 80)

now = datetime.datetime.now(datetime.timezone.utc).replace(tzinfo=None)
print(f"Propagation Time (UTC): {now.strftime('%Y-%m-%d %H:%M:%S')}\n")

ok_count = 0
fail_count = 0

for name, norad_id in SATELLITE_CATALOG.items():
    name_str, l1, l2 = fetch_tle(norad_id)
    if l1 and l2:
        pos = propagate(l1, l2, now)
        if pos:
            bbox = compute_tracking_bbox(pos['lat'], pos['lon'])
            bbox_str = ", ".join([f"{b:.1f}" for b in bbox])
            print(f"  [OK] {name:15s} NORAD {norad_id:5d} | "
                  f"lat={pos['lat']:8.3f} lon={pos['lon']:9.3f} "
                  f"alt={pos['alt_km']:7.1f}km "
                  f"vel={pos['velocity_km_s']:.3f}km/s "
                  f"inc={pos['inclination_deg']:.1f}deg "
                  f"| BBOX=[{bbox_str}]")
            ok_count += 1
        else:
            print(f"  [!!] {name:15s} NORAD {norad_id:5d} | TLE OK but propagation FAILED")
            fail_count += 1
    else:
        print(f"  [XX] {name:15s} NORAD {norad_id:5d} | TLE fetch FAILED")
        fail_count += 1

print(f"\n  Result: {ok_count}/{ok_count + fail_count} satellites tracked successfully")
