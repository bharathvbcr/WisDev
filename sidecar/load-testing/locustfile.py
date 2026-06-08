import random
from locust import HttpUser, task, between

class WisDevUser(HttpUser):
    wait_time = between(1, 5)  # Simulate user think time (1-5 seconds)

    @task(3)
    def health_check(self):
        """High frequency health check ping."""
        self.client.get("/health")

    @task(2)
    def analyze_query(self):
        """Simulate a user asking a research question."""
        queries = [
            "AI in cancer diagnosis",
            "CRISPR gene editing ethics",
            "Quantum computing for drug discovery",
            "Impact of climate change on agriculture",
            "Machine learning for protein folding"
        ]
        query = random.choice(queries)
        self.client.post("/api/wisdev/analyze-query", json={"query": query})

    @task(1)
    def domain_detect(self):
        """Simulate domain detection for routing."""
        self.client.post("/api/wisdev/domain-detect", json={"query": "Neuroscience mechanisms of memory"})

    @task(1)
    def get_static_questions(self):
        """Fetch static clarifying questions."""
        self.client.post("/api/wisdev/questions", json={
            "query": "General biology",
            "question_id": "q1_domain"
        })
