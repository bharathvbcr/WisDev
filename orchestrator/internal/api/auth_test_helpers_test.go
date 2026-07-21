package api

import (
	"context"
	"net/http"
)

func withTestUserID(req *http.Request, userID string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
}

const testWisDevJobUserID = "wisdev-job-test-user"

func withWisDevJobTestUser(req *http.Request) *http.Request {
	return withTestUserID(req, testWisDevJobUserID)
}

func ownedWisDevJobForTest(job *YoloJob) *YoloJob {
	if job != nil {
		job.UserID = testWisDevJobUserID
	}
	return job
}
