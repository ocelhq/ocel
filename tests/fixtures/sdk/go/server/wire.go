package main

import (
	"encoding/json"
	"io"
	"net/http"

	"example.com/web/infra"
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

func writeRefusal(w http.ResponseWriter, err error) {
	writeAnswer(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func handleWire(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/wire/trigger", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("POST /api/wire/send", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("GET /api/wire/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("GET /api/wire/sightings/{tag}", func(w http.ResponseWriter, r *http.Request) {
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

}
