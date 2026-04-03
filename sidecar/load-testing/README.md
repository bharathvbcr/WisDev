# ScholarLM Load Testing

This directory contains load testing scripts using [Locust](https://locust.io/).

## Prerequisites

Install Locust:
```bash
pip install locust
```

## Running the Tests

1. Ensure your API is running (e.g., at http://localhost:8080).
2. Run the load test:

```bash
locust -f locustfile.py --host http://localhost:8080
```

3. Open your browser at http://localhost:8089 to start the test.

## Scenarios

The `locustfile.py` simulates a typical user journey:
- **Health Check (3x freq):** Checks system liveness.
- **Analyze Query (2x freq):** Simulates the "heavy" AI path for query understanding.
- **Domain Detect (1x freq):** Checks the classification router.
- **Get Questions (1x freq):** Checks the static question retrieval path.

## Tips for Scale

To test "thousands of users":
1. Run locust in headless mode:
   ```bash
   locust -f locustfile.py --headless -u 1000 -r 50 --run-time 10m --host http://YOUR_CLOUDRUN_URL
   ```
2. Monitor your Container Service instance count and Redis CPU usage during the test.
