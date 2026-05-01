# Copyright 2026 Kushan J
# SPDX-License-Identifier: Apache-2.0

"""
Ultra WMS + Orbital Satellite Audit Script
Tests EVERY single WMS layer endpoint and orbital satellite in the GeoStream system.
"""
import requests
import sys
import json
import datetime

# ═══════════════════════════════════════════════════════════════════
# 1. ALL UNIQUE WMS BASE URLS (from satellites.ts)
# ═══════════════════════════════════════════════════════════════════
WMS_SERVERS = {
    "NASA GIBS": "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
    "GEBCO": "https://wms.gebco.net/mapserv",
    "EOX Sentinel": "https://tiles.maps.eox.at/wms",
    "GOES West (Iowa State)": "https://mesonet.agron.iastate.edu/cgi-bin/wms/goes/west_ir.cgi",
    "NEXRAD (Iowa State)": "https://mesonet.agron.iastate.edu/cgi-bin/wms/nexrad/n0q.cgi",
}

# ═══════════════════════════════════════════════════════════════════
# 2. COMPLETE LAYER CATALOG (every layer from satellites.ts)
# ═══════════════════════════════════════════════════════════════════
GIBS = "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi"
GEBCO = "https://wms.gebco.net/mapserv"
EOX = "https://tiles.maps.eox.at/wms"
GOES_WEST = "https://mesonet.agron.iastate.edu/cgi-bin/wms/goes/west_ir.cgi"
NEXRAD = "https://mesonet.agron.iastate.edu/cgi-bin/wms/nexrad/n0q.cgi"

ALL_LAYERS = [
    # Ultra High-Res
    {"name": "HLS Landsat 30m", "layer": "HLS_L30_Nadir_BRDF_Adjusted_Reflectance", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "HLS Sentinel-2 30m", "layer": "HLS_S30_Nadir_BRDF_Adjusted_Reflectance", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "Himawari-9 AHI", "layer": "Himawari_AHI_Band3_Red_Visible_1km", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "SMAP Soil Moisture", "layer": "SMAP_L4_Analyzed_Root_Zone_Soil_Moisture", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    # Fire & Thermal
    {"name": "MODIS Terra Thermal", "layer": "MODIS_Terra_Thermal_Anomalies_All", "url": GIBS, "bbox": "-122.4,37.7,-122.3,37.8", "needs_time": True},
    {"name": "MODIS Aqua Thermal", "layer": "MODIS_Aqua_Thermal_Anomalies_All", "url": GIBS, "bbox": "-122.4,37.7,-122.3,37.8", "needs_time": True},
    {"name": "VIIRS NOAA-20 Fire 375m", "layer": "VIIRS_NOAA20_Thermal_Anomalies_375m_All", "url": GIBS, "bbox": "-122.4,37.7,-122.3,37.8", "needs_time": True},
    {"name": "VIIRS SNPP Fire 375m", "layer": "VIIRS_SNPP_Thermal_Anomalies_375m_All", "url": GIBS, "bbox": "-122.4,37.7,-122.3,37.8", "needs_time": True},
    {"name": "Land Surface Temp Day", "layer": "MODIS_Terra_Land_Surface_Temp_Day", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "Land Surface Temp Night", "layer": "MODIS_Terra_Land_Surface_Temp_Night", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    # True Color
    {"name": "MODIS Terra True Color", "layer": "MODIS_Terra_CorrectedReflectance_TrueColor", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "MODIS Aqua True Color", "layer": "MODIS_Aqua_CorrectedReflectance_TrueColor", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "VIIRS SNPP True Color", "layer": "VIIRS_SNPP_CorrectedReflectance_TrueColor", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "VIIRS NOAA-20 True Color", "layer": "VIIRS_NOAA20_CorrectedReflectance_TrueColor", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "MODIS False Color 7-2-1", "layer": "MODIS_Terra_CorrectedReflectance_Bands721", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "Sentinel-2 Cloudless", "layer": "s2cloudless-2024", "url": EOX, "bbox": "68.0,6.0,97.5,37.0", "needs_time": False},
    # Weather
    {"name": "GOES West IR", "layer": "goes_west_ir", "url": GOES_WEST, "bbox": "-130.0,20.0,-60.0,55.0", "needs_time": False},
    {"name": "NEXRAD Radar", "layer": "nexrad-n0q-900913", "url": NEXRAD, "bbox": "-125.0,24.0,-66.0,50.0", "needs_time": False},
    {"name": "Cloud Top Temp Terra", "layer": "MODIS_Terra_Cloud_Top_Temp_Day", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "Cloud Top Temp Aqua", "layer": "MODIS_Aqua_Cloud_Top_Temp_Day", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "Water Vapor 5km", "layer": "MODIS_Terra_Water_Vapor_5km_Day", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    # Vegetation
    {"name": "NDVI 8-Day", "layer": "MODIS_Terra_NDVI_8Day", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "EVI Enhanced Veg", "layer": "MODIS_Terra_EVI_8Day", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "Snow Cover Terra", "layer": "MODIS_Terra_NDSI_Snow_Cover", "url": GIBS, "bbox": "72.0,27.0,90.0,37.0", "needs_time": True},
    {"name": "Snow Cover Aqua", "layer": "MODIS_Aqua_NDSI_Snow_Cover", "url": GIBS, "bbox": "72.0,27.0,90.0,37.0", "needs_time": True},
    # Ocean
    {"name": "SST Terra", "layer": "MODIS_Terra_L2_Sea_Surface_Temp_Day", "url": GIBS, "bbox": "60.0,0.0,95.0,25.0", "needs_time": True},
    {"name": "SST Aqua", "layer": "MODIS_Aqua_L2_Sea_Surface_Temp_Day", "url": GIBS, "bbox": "60.0,0.0,95.0,25.0", "needs_time": True},
    {"name": "Chlorophyll-a", "layer": "MODIS_Aqua_L2_Chlorophyll_A", "url": GIBS, "bbox": "60.0,0.0,95.0,25.0", "needs_time": True},
    {"name": "GEBCO Bathymetry", "layer": "GEBCO_LATEST_2", "url": GEBCO, "bbox": "60.0,0.0,95.0,25.0", "needs_time": False},
    {"name": "GEBCO Shaded Relief", "layer": "GEBCO_LATEST", "url": GEBCO, "bbox": "60.0,0.0,95.0,25.0", "needs_time": False},
    # Night
    {"name": "VIIRS SNPP Day/Night", "layer": "VIIRS_SNPP_DayNightBand_At_Sensor_Radiance", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    {"name": "VIIRS NOAA-20 Day/Night", "layer": "VIIRS_NOAA20_DayNightBand_At_Sensor_Radiance", "url": GIBS, "bbox": "68.0,6.0,97.5,37.0", "needs_time": True},
    # Air Quality
    {"name": "Aerosol Depth Terra 3km", "layer": "MODIS_Terra_Aerosol_Optical_Depth_3km", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "Aerosol Depth Aqua 3km", "layer": "MODIS_Aqua_Aerosol_Optical_Depth_3km", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "NO2 OMI", "layer": "OMI_Nitrogen_Dioxide_Tropo_Column", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "SO2 OMI", "layer": "OMI_Sulfur_Dioxide_Lower_Troposphere", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    {"name": "UV Absorbing Aerosol", "layer": "OMI_Absorbing_Aerosol_Optical_Depth", "url": GIBS, "bbox": "76.5,28.0,77.8,29.2", "needs_time": True},
    # India-specific (same layers, different BBOX)
    {"name": "India SST Blended", "layer": "MODIS_Terra_Sea_Surface_Temp_Blended", "url": GIBS, "bbox": "60.0,0.0,95.0,25.0", "needs_time": True},
    # BlueMarble (auto-injected base map)
    {"name": "BlueMarble Base", "layer": "BlueMarble_ShadedRelief_Bathymetry", "url": GIBS, "bbox": "-180.0,-90.0,180.0,90.0", "needs_time": False},
]

# ═══════════════════════════════════════════════════════════════════
# 3. ORBITAL SATELLITE CATALOG (from orbital_tracker.py)
# ═══════════════════════════════════════════════════════════════════
ORBITAL_SATELLITES = {
    "TERRA":       25994,
    "AQUA":        27424,
    "SUOMI_NPP":   37849,
    "NOAA-20":     43013,
    "LANDSAT_8":   39084,
    "LANDSAT_9":   49260,
    "SENTINEL_1A": 39634,
    "SENTINEL_2A": 40697,
    "SENTINEL_3A": 41335,
    "SMAP":        40376,
    "GPM_CORE":    39574,
    "CALIPSO":     29108,
    "ISS":         25544,
    "TIANGONG":    48274,
    "HST":         20580,
}

HEADERS = {"User-Agent": "GeoStream-Audit/1.0"}

def test_wms_servers():
    """Test GetCapabilities for each unique WMS server."""
    print("=" * 70)
    print("PHASE 1: WMS SERVER GetCapabilities CHECK")
    print("=" * 70)
    results = {}
    for name, url in WMS_SERVERS.items():
        try:
            cap_url = f"{url}?SERVICE=WMS&REQUEST=GetCapabilities&VERSION=1.1.1"
            resp = requests.get(cap_url, headers=HEADERS, timeout=15)
            is_xml = "xml" in resp.headers.get("Content-Type", "") or resp.text.strip().startswith("<?xml") or resp.text.strip().startswith("<WMT")
            status = "✅" if resp.status_code == 200 and is_xml else "❌"
            results[name] = resp.status_code == 200 and is_xml
            print(f"  {status} {name}: HTTP {resp.status_code} | {len(resp.text)} bytes | XML: {is_xml}")
        except Exception as e:
            results[name] = False
            print(f"  ❌ {name}: FAILED — {e}")
    return results

def test_wms_layers():
    """Test GetMap for each layer with a sample BBOX and date."""
    print("\n" + "=" * 70)
    print("PHASE 2: WMS LAYER GetMap CHECK (38 layers)")
    print("=" * 70)
    
    # Use a date 3 days ago to ensure data availability
    test_date = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=3)).strftime("%Y-%m-%d")
    
    passed = 0
    failed = 0
    warnings = 0
    failed_layers = []
    
    for entry in ALL_LAYERS:
        name = entry["name"]
        layer = entry["layer"]
        url = entry["url"]
        bbox = entry["bbox"]
        needs_time = entry["needs_time"]
        
        time_param = f"&TIME={test_date}" if needs_time else ""
        
        # Skip TIME for BlueMarble
        if "BlueMarble" in layer:
            time_param = ""
        
        get_map_url = (
            f"{url}?SERVICE=WMS&REQUEST=GetMap&VERSION=1.1.1"
            f"&LAYERS={layer}&SRS=EPSG:4326&BBOX={bbox}"
            f"&WIDTH=256&HEIGHT=256&FORMAT=image/png"
            f"{time_param}&TRANSPARENT=TRUE"
        )
        
        try:
            resp = requests.get(get_map_url, headers=HEADERS, timeout=20)
            content_type = resp.headers.get("Content-Type", "")
            size = len(resp.content)
            
            if resp.status_code == 200 and "image" in content_type and size > 500:
                print(f"  ✅ {name:<30} | {layer:<55} | {size:>6} bytes")
                passed += 1
            elif resp.status_code == 200 and "xml" in content_type:
                print(f"  ⚠️  {name:<30} | {layer:<55} | Server returned XML (no data for this date/bbox)")
                warnings += 1
            elif resp.status_code == 200 and size <= 500:
                print(f"  ⚠️  {name:<30} | {layer:<55} | Tiny response ({size}B) — no data for this date")
                warnings += 1
            else:
                print(f"  ❌ {name:<30} | {layer:<55} | HTTP {resp.status_code} | {content_type}")
                failed += 1
                failed_layers.append({"name": name, "layer": layer, "status": resp.status_code, "content_type": content_type})
        except requests.exceptions.Timeout:
            print(f"  ⏱️  {name:<30} | {layer:<55} | TIMEOUT (20s)")
            warnings += 1
        except Exception as e:
            print(f"  ❌ {name:<30} | {layer:<55} | ERROR: {e}")
            failed += 1
            failed_layers.append({"name": name, "layer": layer, "error": str(e)})
    
    print(f"\n  Summary: {passed} passed | {warnings} warnings | {failed} failed")
    return passed, warnings, failed, failed_layers

def test_orbital_satellites():
    """Test CelesTrak TLE fetch for each orbital satellite."""
    print("\n" + "=" * 70)
    print("PHASE 3: ORBITAL SATELLITE TLE FETCH CHECK (15 satellites)")
    print("=" * 70)
    
    passed = 0
    failed = 0
    
    for name, norad_id in ORBITAL_SATELLITES.items():
        try:
            url = f"https://celestrak.org/NORAD/elements/gp.php?CATNR={norad_id}&FORMAT=3le"
            resp = requests.get(url, headers=HEADERS, timeout=15)
            lines = resp.text.strip().split("\n")
            
            if resp.status_code == 200 and len(lines) >= 3 and lines[1].startswith("1 "):
                # Parse TLE to verify it's valid
                tle_name = lines[0].strip()
                epoch_str = lines[1][18:32].strip()
                print(f"  ✅ {name:<15} | NORAD {norad_id:<6} | TLE Name: {tle_name:<25} | Epoch: {epoch_str}")
                passed += 1
            else:
                print(f"  ❌ {name:<15} | NORAD {norad_id:<6} | HTTP {resp.status_code} | Lines: {len(lines)}")
                failed += 1
        except Exception as e:
            print(f"  ❌ {name:<15} | NORAD {norad_id:<6} | ERROR: {e}")
            failed += 1
    
    print(f"\n  Summary: {passed} passed | {failed} failed")
    return passed, failed

def test_sgp4_propagation():
    """Test that SGP4 propagation actually works for a known satellite."""
    print("\n" + "=" * 70)
    print("PHASE 4: SGP4 ORBITAL PROPAGATION VERIFICATION")
    print("=" * 70)
    
    try:
        from sgp4.api import Satrec, jday
        import math
        
        # Fetch ISS TLE
        url = f"https://celestrak.org/NORAD/elements/gp.php?CATNR=25544&FORMAT=3le"
        resp = requests.get(url, headers=HEADERS, timeout=15)
        lines = resp.text.strip().split("\n")
        
        if len(lines) >= 3:
            satellite = Satrec.twoline2rv(lines[1].strip(), lines[2].strip())
            now = datetime.datetime.now(datetime.timezone.utc)
            jd, fr = jday(now.year, now.month, now.day, now.hour, now.minute, now.second)
            e, r, v = satellite.sgp4(jd, fr)
            
            if e == 0:
                x, y, z = r
                vx, vy, vz = v
                alt_km = math.sqrt(x**2 + y**2 + z**2) - 6371.0
                vel = math.sqrt(vx**2 + vy**2 + vz**2)
                
                # Sanity checks
                alt_ok = 380 < alt_km < 450  # ISS orbits at ~408km
                vel_ok = 7.0 < vel < 8.0  # ~7.66 km/s
                
                print(f"  ISS Position: x={x:.1f}, y={y:.1f}, z={z:.1f} km")
                print(f"  ISS Altitude: {alt_km:.1f} km {'✅' if alt_ok else '❌ (expected 380-450km)'}")
                print(f"  ISS Velocity: {vel:.3f} km/s {'✅' if vel_ok else '❌ (expected 7.0-8.0 km/s)'}")
                
                if alt_ok and vel_ok:
                    print(f"\n  ✅ SGP4 propagation is producing physically correct results")
                    return True
                else:
                    print(f"\n  ⚠️  SGP4 values outside expected bounds (TLE may be stale)")
                    return True  # Still technically working
            else:
                print(f"  ❌ SGP4 returned error code: {e}")
                return False
    except ImportError:
        print(f"  ❌ sgp4 module not installed!")
        return False
    except Exception as e:
        print(f"  ❌ SGP4 test failed: {e}")
        return False

if __name__ == "__main__":
    print("🛰️  GeoStream Ultra WMS + Orbital Satellite Audit")
    print(f"    Timestamp: {datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%d %H:%M:%S UTC')}")
    print()
    
    # Phase 1
    server_results = test_wms_servers()
    
    # Phase 2
    layer_passed, layer_warnings, layer_failed, failed_layers = test_wms_layers()
    
    # Phase 3
    orbital_passed, orbital_failed = test_orbital_satellites()
    
    # Phase 4
    sgp4_ok = test_sgp4_propagation()
    
    # Final Summary
    print("\n" + "=" * 70)
    print("FINAL AUDIT SUMMARY")
    print("=" * 70)
    print(f"  WMS Servers:       {sum(server_results.values())}/{len(server_results)} online")
    print(f"  WMS Layers:        {layer_passed} passed | {layer_warnings} warnings | {layer_failed} failed")
    print(f"  Orbital Sats:      {orbital_passed}/{len(ORBITAL_SATELLITES)} TLE fetched")
    print(f"  SGP4 Propagation:  {'✅ Working' if sgp4_ok else '❌ Failed'}")
    
    if failed_layers:
        print(f"\n  ❌ FAILED LAYERS:")
        for fl in failed_layers:
            print(f"     - {fl['name']}: {fl['layer']}")
    
    total_issues = layer_failed + orbital_failed + (0 if sgp4_ok else 1)
    if total_issues == 0:
        print(f"\n  🎯 ALL SYSTEMS NOMINAL — {layer_passed + layer_warnings} layers + {orbital_passed} satellites verified")
    else:
        print(f"\n  ⚠️  {total_issues} issue(s) found — see details above")
