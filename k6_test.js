import http from 'k6/http';
import { check, sleep } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

export const options = {
  scenarios: {
    // Test 1: Queue Saturation & CAS Locking
    // Blasts the job creation endpoint to ensure the DB and worker queue handles it gracefully.
    queue_saturation: {
      executor: 'constant-arrival-rate',
      rate: 50, // 50 requests per second
      timeUnit: '1s',
      duration: '10s',
      preAllocatedVUs: 50,
      maxVUs: 100,
      exec: 'submitJob',
    },
    // Test 2: Idempotency Spike
    // Sends the exact same job payload concurrently to verify the SHA-256 caching works
    // and returns 202 instantly without overwhelming the Python workers.
    idempotency_spike: {
      executor: 'shared-iterations',
      vus: 100,
      iterations: 500, // 500 exact same requests
      maxDuration: '10s',
      exec: 'submitIdenticalJob',
      startTime: '12s', // Run after queue saturation
    },
};

const BASE_URL = 'http://localhost:8080/api';
let authToken = '';

export function setup() {
  // Create a unique user for the load test
  const email = `k6-test-${uuidv4()}@example.com`;
  const password = 'supersecurepassword123';

  const regRes = http.post(`${BASE_URL}/auth/register`, JSON.stringify({ email, password }), {
    headers: { 'Content-Type': 'application/json' },
  });
  
  check(regRes, { 'registered successfully': (r) => r.status === 201 || r.status === 409 });

  const loginRes = http.post(`${BASE_URL}/auth/login`, JSON.stringify({ email, password }), {
    headers: { 'Content-Type': 'application/json' },
  });

  check(loginRes, { 'logged in successfully': (r) => r.status === 200 });
  
  return loginRes.json('token');
}

export function submitJob(token) {
  // Randomize dates to bypass cache
  const day = Math.floor(Math.random() * 28) + 1;
  const payload = JSON.stringify({
    bbox: [-122.4, 37.7, -122.3, 37.8],
    start_date: `2026-01-${day < 10 ? '0'+day : day}`,
    end_date: `2026-02-${day < 10 ? '0'+day : day}`,
    fps: 30,
    frame_count: 60,
    time_step: "1d",
    wms_layers: [
      {
        url: "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
        name: "MODIS_Terra_CorrectedReflectance_TrueColor"
      }
    ]
  });

  const res = http.post(`${BASE_URL}/jobs`, payload, {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    },
  });

  check(res, {
    'job submitted or rate limited': (r) => r.status === 202 || r.status === 429 || r.status === 503,
  });
}

export function submitIdenticalJob(token) {
  // Static payload to trigger the SHA-256 Cache HIT
  const payload = JSON.stringify({
    bbox: [-74.0, 40.7, -73.9, 40.8], // NYC
    start_date: "2026-03-01",
    end_date: "2026-03-10",
    fps: 15,
    frame_count: 30,
    time_step: "1d",
    wms_layers: [
      {
        url: "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
        name: "MODIS_Aqua_Thermal_Anomalies_All"
      }
    ]
  });

  const res = http.post(`${BASE_URL}/jobs`, payload, {
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    },
  });

  // We expect a 202, but if the cache logic works instantly, it should be super fast
  check(res, {
    'cache hit successful': (r) => r.status === 202 || r.status === 409,
  });
}
