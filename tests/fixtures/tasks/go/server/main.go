package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"example.com/tasks/infra"
	"ocel.dev"
)

type runText struct {
	ID      string `json:"id"`
	Task    string `json:"task"`
	Status  string `json:"status"`
	Payload string `json:"payload"`
	Output  string `json:"output"`
	Error   string `json:"error"`
}

func writeAnswer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeRefusal(w http.ResponseWriter, err error) {
	writeAnswer(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func describeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3109"
	}

	http.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeAnswer(w, http.StatusOK, map[string]any{"ok": true, "app": "web"})
	})

	http.HandleFunc("GET /api/wire/names", func(w http.ResponseWriter, _ *http.Request) {
		writeAnswer(w, http.StatusOK, map[string]infra.Names{"task": infra.ReadTaskNames(), "topic": infra.ReadTopicNames()})
	})

	http.HandleFunc("POST /api/wire/trigger", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeRefusal(w, err)
			return
		}
		since := infra.CountRequests()
		handle, err := infra.ExactEcho.Trigger(r.Context(), json.RawMessage(body))
		writeAnswer(w, http.StatusOK, map[string]any{"id": handle.ID, "error": describeError(err), "requests": infra.ListRequestsSince(since)})
	})

	http.HandleFunc("POST /api/wire/send", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeRefusal(w, err)
			return
		}
		since := infra.CountRequests()
		messageID, err := infra.ExactOrders.Send(r.Context(), json.RawMessage(body))
		writeAnswer(w, http.StatusOK, map[string]any{"messageId": messageID, "error": describeError(err), "requests": infra.ListRequestsSince(since)})
	})

	http.HandleFunc("GET /api/wire/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		run, err := ocel.RetrieveRun(r.Context(), r.PathValue("id"))
		if err != nil {
			writeRefusal(w, err)
			return
		}
		writeAnswer(w, http.StatusOK, runText{
			ID: run.ID, Task: run.Task, Status: string(run.Status),
			Payload: string(run.Payload), Output: string(run.Output), Error: run.Error,
		})
	})

	http.HandleFunc("GET /api/wire/sightings/{tag}", func(w http.ResponseWriter, r *http.Request) {
		page, err := ocel.ListRuns(r.Context(), ocel.ForTask(infra.SightingTask.Name()), ocel.Tags(r.PathValue("tag")), ocel.Statuses(ocel.RunCompleted))
		if err != nil {
			writeRefusal(w, err)
			return
		}
		sightings := []infra.Sighting{}
		for _, run := range page.Runs {
			var sighting infra.Sighting
			if err := json.Unmarshal(run.Output, &sighting); err != nil {
				writeRefusal(w, err)
				return
			}
			sightings = append(sightings, sighting)
		}
		writeAnswer(w, http.StatusOK, sightings)
	})

	http.HandleFunc("GET /api/wire/envelopes/{tag}", func(w http.ResponseWriter, r *http.Request) {
		page, err := ocel.ListRuns(r.Context(), ocel.ForTask(infra.EnvelopeTask.Name()), ocel.Tags(r.PathValue("tag")), ocel.Statuses(ocel.RunCompleted))
		if err != nil {
			writeRefusal(w, err)
			return
		}
		envelopes := []string{}
		for _, run := range page.Runs {
			var recorded infra.RecordedEnvelope
			if err := json.Unmarshal(run.Output, &recorded); err != nil {
				writeRefusal(w, err)
				return
			}
			envelopes = append(envelopes, recorded.RecordedEnvelope)
		}
		writeAnswer(w, http.StatusOK, envelopes)
	})

	log.Printf("tasks go listening on http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
