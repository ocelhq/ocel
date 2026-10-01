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

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3109"
	}

	http.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeAnswer(w, http.StatusOK, map[string]any{"ok": true, "app": "web"})
	})

	http.HandleFunc("POST /api/tasks/exact-echo/trigger", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeRefusal(w, err)
			return
		}
		handle, err := infra.ExactEcho.Trigger(r.Context(), json.RawMessage(body))
		if err != nil {
			writeRefusal(w, err)
			return
		}
		writeAnswer(w, http.StatusOK, map[string]string{"id": handle.ID})
	})

	http.HandleFunc("POST /api/topics/exact-orders/send", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeRefusal(w, err)
			return
		}
		messageID, err := infra.ExactOrders.Send(r.Context(), json.RawMessage(body))
		if err != nil {
			writeRefusal(w, err)
			return
		}
		writeAnswer(w, http.StatusOK, map[string]string{"messageId": messageID})
	})

	http.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
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

	http.HandleFunc("GET /api/seen/{tag}", func(w http.ResponseWriter, r *http.Request) {
		page, err := ocel.ListRuns(r.Context(), ocel.ForTask(infra.SeenTask.Name()), ocel.Tags(r.PathValue("tag")), ocel.Statuses(ocel.RunCompleted))
		if err != nil {
			writeRefusal(w, err)
			return
		}
		seen := []infra.Sighting{}
		for _, run := range page.Runs {
			var one infra.Sighting
			if err := json.Unmarshal(run.Output, &one); err != nil {
				writeRefusal(w, err)
				return
			}
			seen = append(seen, one)
		}
		writeAnswer(w, http.StatusOK, seen)
	})

	log.Printf("tasks go listening on http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
