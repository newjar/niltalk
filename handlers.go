package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi"
	"github.com/gorilla/websocket"
	"github.com/knadh/niltalk/internal/hub"
	"github.com/knadh/niltalk/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultDateFormatForRequestParam = "2006-01-02 15:04:05.999999 -0700"
	dateFormat = "2006-01-02"
	hasAuth = 1 << iota
	hasRoom
)

type sess struct {
	ID     string
	Handle string
}

// reqCtx is the context injected into every request.
type reqCtx struct {
	app    *App
	room   *hub.Room
	roomID string
	sess   sess
}

// jsonResp is the envelope for all JSON API responses.
type jsonResp struct {
	Error *string     `json:"error"`
	Data  interface{} `json:"data"`
}

// tplWrap is the envelope for all HTML template executions.
type tpl struct {
	Config *hub.Config
	Data   tplData
}

type tplData struct {
	Title       string
	Description string
	Room        interface{}
	Auth        bool
}

type reqRoom struct {
	Name     string `json:"name"`
	Handle   string `json:"handle"`
	Password string `json:"password"`
}

type reqChatHistory struct {
	From  string `json:"from"`
	Until string `json:"until"`
	Type string `json:"type"`
	Hours string `json:"hours"`
	TimeZone string `json:"timezone"`
	InCSV string `json:"in_csv"`
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
	return true
}}

// handleIndex renders the homepage.
func handleIndex(w http.ResponseWriter, r *http.Request) {
	var (
		ctx = r.Context().Value("ctx").(*reqCtx)
		app = ctx.app
	)
	respondHTML("index", tplData{
		Title: app.cfg.Name,
	}, http.StatusOK, w, app)
}

// handleRoomPage renders the chat room page.
func handleRoomPage(w http.ResponseWriter, r *http.Request) {
	var (
		ctx  = r.Context().Value("ctx").(*reqCtx)
		app  = ctx.app
		room = ctx.room
	)

	if room == nil {
		respondHTML("room-not-found", tplData{}, http.StatusNotFound, w, app)
		return
	}

	out := tplData{
		Title: room.Name,
		Room:  room,
	}
	if ctx.sess.ID != "" {
		out.Auth = true
	}

	// Disable browser caching.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	respondHTML("room", out, http.StatusOK, w, app)
}

// handleLogin authenticates a peer into a room.
func handleLogin(w http.ResponseWriter, r *http.Request) {
	var (
		err error
		ctx  = r.Context().Value("ctx").(*reqCtx)
		app  = ctx.app
		room = ctx.room
		roomID = ctx.roomID
	)

	var req reqRoom
	if err := readJSONReq(r, &req); err != nil {
		respondJSON(w, nil, errors.New("error parsing JSON request"), http.StatusBadRequest)
		return
	}

	//Create room if room is not existed
	if room == nil {
		pwdHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 8)
		if err != nil {
			app.logger.Printf("error hashing password: %v", err)
			respondJSON(w, "Error hashing password", nil, http.StatusInternalServerError)
			return
		}

		room, err = app.hub.AddRoom(roomID, roomID, pwdHash)
		if err != nil {
			respondJSON(w, nil, err, http.StatusInternalServerError)
			return
		}
	}

	// Validate password.
	if err := bcrypt.CompareHashAndPassword(room.Password, []byte(req.Password)); err != nil {
		respondJSON(w, nil, errors.New("incorrect password"), http.StatusForbidden)
		return
	}

	// Register a new session for the peer in the DB.
	sessID, err := hub.GenerateGUID(32)
	if err != nil {
		app.logger.Printf("error generating session ID: %v", err)
		respondJSON(w, nil, errors.New("error generating session ID"), http.StatusInternalServerError)
		return
	}

	if err := app.hub.Store.AddSession(sessID, req.Handle, room.ID, app.cfg.RoomAge); err != nil {
		app.logger.Printf("error creating session: %v", err)
		respondJSON(w, nil, errors.New("error creating session"), http.StatusInternalServerError)
		return
	}

	// Set the session cookie.
	ck := &http.Cookie{Name: app.cfg.SessionCookie, Value: sessID, Path: "/"}
	http.SetCookie(w, ck)
	respondJSON(w, map[string]interface{}{
		app.cfg.SessionCookie: sessID,
	}, nil, http.StatusOK)
}

// handleLogout logs out a peer.
func handleLogout(w http.ResponseWriter, r *http.Request) {
	var (
		ctx  = r.Context().Value("ctx").(*reqCtx)
		app  = ctx.app
		room = ctx.room
	)

	if room == nil {
		respondJSON(w, nil, errors.New("room is invalid or has expired"), http.StatusBadRequest)
		return
	}

	if err := app.hub.Store.RemoveSession(ctx.sess.ID, room.ID); err != nil {
		app.logger.Printf("error removing session: %v", err)
		respondJSON(w, nil, errors.New("error removing session"), http.StatusInternalServerError)
		return
	}

	// Delete the session cookie.
	ck := &http.Cookie{Name: app.cfg.SessionCookie, Value: "", MaxAge: -1, Path: "/"}
	http.SetCookie(w, ck)
	respondJSON(w, true, nil, http.StatusOK)
}

//handleChatHistory handles request for chat history in requested time
func handleChatHistory(w http.ResponseWriter, r *http.Request) {
	var (
		ctx = r.Context().Value("ctx").(*reqCtx)
		app = ctx.app
		room = ctx.room
	)

	if room == nil {
		respondJSON(w, nil, errors.New("room is invalid or has expired"), http.StatusBadRequest)
		return
	}

	req := reqChatHistory{
		From:  r.URL.Query().Get("from"),
		Until: r.URL.Query().Get("until"),
		Type: r.URL.Query().Get("type"),
		Hours: r.URL.Query().Get("hours"),
		TimeZone: r.URL.Query().Get("timezone"),
		InCSV: r.URL.Query().Get("in_csv"),
	}

	start, err := time.Parse(dateFormat, req.From)

	if err != nil {
		respondJSON(w, nil, errors.New("error parsing JSON request for \"from\" param"), http.StatusBadRequest)
		return
	}

	end, err := time.Parse(dateFormat, req.Until)

	if err != nil {
		respondJSON(w, nil, errors.New("error parsing JSON request for \"until\" param"), http.StatusBadRequest)
		return
	}

	if start.After(end) {
		respondJSON(w, nil, errors.New("date of  \"from\" can't be after \"until\""), http.StatusBadRequest)
		return
	}

	var timeLocation *time.Location
	if req.TimeZone != "" {
		timeLocation, _ = time.LoadLocation(req.TimeZone)
	}

	if timeLocation == nil {
		timeLocation = time.UTC
	}

	hourArray := strings.Split(req.Hours, ",")
	dateFilters := []store.DateFilter{}
	now := time.Now()
	nowStart := now.Truncate(24*time.Hour)
	nowEnd := nowStart.Add(23 * time.Hour + 59*time.Minute + 59*time.Second)
	app.logger.Printf("nowStart: %s; nowEnd: %s ",nowStart, nowEnd)
	
	dateLoop:
	for i := start; !i.After(end); i = i.AddDate(0, 0, 1) {
		if len(hourArray) <= 0 || req.Hours == "" {
			dateFilter := store.DateFilter{
				Start: time.Date(i.Year(), i.Month(), i.Day(), nowStart.In(timeLocation).Hour(), nowStart.In(timeLocation).Minute(), nowStart.In(timeLocation).Second(),nowStart.In(timeLocation).Nanosecond(), timeLocation),
				End:   time.Date(i.Year(), i.Month(), i.Day(), nowEnd.In(timeLocation).Hour(), nowEnd.In(timeLocation).Minute(), nowEnd.In(timeLocation).Second(), nowEnd.In(timeLocation).Nanosecond(), timeLocation),
			}

			if !dateFilter.Start.Before(dateFilter.End) {
				dateFilter.End = dateFilter.End.AddDate(0, 0, 1)
			}

			if now.Before(dateFilter.End.UTC()) {
				dateFilter.End = now.In(timeLocation)
			}

			if dateFilter.Start.UTC().After(now.UTC()) {
				break dateLoop
			}

			dateFilters = append(dateFilters, dateFilter)

			continue
		}

		for _, hour := range hourArray {
			var start, end time.Time
			start, err = time.Parse(defaultDateFormatForRequestParam, i.Format(dateFormat)+" "+strings.Split(hour, "-")[0])

			if err == nil {
				end, err = time.Parse(defaultDateFormatForRequestParam, i.Format(dateFormat)+" "+strings.Split(hour, "-")[1])
			}

			if err != nil {
				respondJSON(w, nil, errors.New("Error parsing hours for date"), http.StatusBadRequest)
				return
			}

			if end.UTC().Before(start.UTC()) {
				end = end.AddDate(0, 0, 1)
			}

			if now.Before(end) {
				end = now.In(timeLocation)
			}

			if start.UTC().After(now.UTC()) {
				break dateLoop
			}

			dateFilters = append(dateFilters, store.DateFilter{
				Start: start.In(timeLocation),
				End:   end.In(timeLocation),
			})
		}
	}

	sort.SliceStable(dateFilters, func(i, j int) bool {
		return dateFilters[i].Start.Before(dateFilters[j].Start)
	})
 
	chatHistory, err := app.hub.GetChatHistory(room.ID,dateFilters...)

	if err != nil {
		app.logger.Printf("Error get chat history : %s",err)
		respondJSON(w, nil, err, http.StatusInternalServerError)
		return
	}

	dataToCSV := [][]string{
		{
			"TIMESTAMP",
			"MESSAGE",
			"PEER",
			"PEER_ID",
		},
	}

	if req.InCSV == "true" {
		b := &bytes.Buffer{}
		wr := csv.NewWriter(b)
		
		for _, chat := range chatHistory{
			js, _:= json.Marshal(chat.Data)
			data := make(map[string]interface{})
			json.Unmarshal([]byte(js), &data)
			dataToCSV = append(dataToCSV, []string{
				chat.Timestamp.Format(defaultDateFormatForRequestParam),
				data["message"].(string),
				data["peer_handle"].(string),
				data["peer_id"].(string),
			})
		}

		fileName := "Chat_Report_"+req.From+"_to_"+req.Until+".csv"

		wr.WriteAll(dataToCSV)
		wr.Flush()

		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment;filename="+fileName)
		w.Write(b.Bytes())
	} else {
		respondJSON(w, chatHistory, nil, http.StatusOK)
	}
}

// handleWS handles incoming connections.
func handleWS(w http.ResponseWriter, r *http.Request) {
	var (
		ctx  = r.Context().Value("ctx").(*reqCtx)
		app  = ctx.app
		room = ctx.room
	)

	if ctx.sess.ID == "" {
		app.logger.Printf("Handle Websocket failed: %s : invalid session",r.RemoteAddr)
		respondJSON(w, nil, errors.New("invalid session"), http.StatusForbidden)
		return
	}

	// Create the WS connection.
	upgrader.CheckOrigin = func(_ *http.Request) bool {
		return true
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		app.logger.Printf("Websocket upgrade failed: %s: %v", r.RemoteAddr, err)
		return
	}

	// Create a new peer instance and add to the room.
	room.AddPeer(ctx.sess.ID, ctx.sess.Handle, ws)
}

// respondJSON responds to an HTTP request with a generic payload or an error.
func respondJSON(w http.ResponseWriter, data interface{}, err error, statusCode int) {
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	w.WriteHeader(statusCode)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	out := jsonResp{Data: data}
	if err != nil {
		e := err.Error()
		out.Error = &e
	}
	b, err := json.Marshal(out)
	if err != nil {
		logger.Printf("error marshalling JSON response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Write(b)
}

// respondHTML responds to an HTTP request with the HTML output of a given template.
func respondHTML(tplName string, data tplData, statusCode int, w http.ResponseWriter, app *App) {
	if statusCode > 0 {
		w.WriteHeader(statusCode)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := app.tpl.ExecuteTemplate(w, tplName, tpl{
		Config: app.cfg,
		Data:   data,
	})
	if err != nil {
		app.logger.Printf("error rendering template %s: %s", tplName, err)
		w.Write([]byte("error rendering template"))
	}
}

// handleCreateRoom handles the creation of a new room.
func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var (
		ctx = r.Context().Value("ctx").(*reqCtx)
		app = ctx.app
	)

	var req reqRoom
	if err := readJSONReq(r, &req); err != nil {
		respondJSON(w, nil, errors.New("error parsing JSON request"), http.StatusBadRequest)
		return
	}

	if req.Name != "" && (len(req.Name) < 3 || len(req.Name) > 100) {
		respondJSON(w, nil, errors.New("invalid room name (6 - 100 chars)"), http.StatusBadRequest)
		return
	}

	if len(req.Password) < 6 || len(req.Password) > 100 {
		respondJSON(w, nil, errors.New("invalid password (6 - 100 chars)"), http.StatusBadRequest)
		return
	}

	// Hash the password.
	pwdHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 8)
	if err != nil {
		app.logger.Printf("error hashing password: %v", err)
		respondJSON(w, "Error hashing password", nil, http.StatusInternalServerError)
		return
	}

	// Create and activate the new room.
	room, err := app.hub.AddRoom("", req.Name, pwdHash)
	if err != nil {
		respondJSON(w, nil, err, http.StatusInternalServerError)
		return
	}

	respondJSON(w, struct {
		ID string `json:"id"`
	}{room.ID}, nil, http.StatusOK)
}

// wrap is a middleware that handles auth and room check for various HTTP handlers.
// It attaches the app and room contexts to handlers.
func wrap(next http.HandlerFunc, app *App, opts uint8) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var (
			req    = &reqCtx{app: app}
			roomID = chi.URLParam(r, "roomID")
		)

		req.roomID = roomID

		// Check if the request is authenticated.
		if opts&hasAuth != 0 {
			ck, _ := r.Cookie(app.cfg.SessionCookie)
			if (ck != nil && ck.Value != "") || r.URL.Query().Get("session_id") != "" || r.Header.Get("session_id") != "" {
				session := ""

				if (ck == nil || ck.Value == "") && r.URL.Query().Get("session_id") != "" {
					session = r.URL.Query().Get("session_id")
				}else if (ck == nil || ck.Value == "") && r.URL.Query().Get("session_id") != "" {
					session = r.Header.Get("session_id")
				}else {
					session = ck.Value
				}

				s, err := app.hub.Store.GetSession(session, roomID)
				if err != nil {
					fmt.Printf("error checking session: %v", err)
					app.logger.Printf("error checking session: %v", err)
					respondJSON(w, nil, errors.New("error checking session"), http.StatusForbidden)
					return
				}
				req.sess = sess{
					ID:     s.ID,
					Handle: s.Handle,
				}
			}
		}

		// Check if the room is valid and active.
		if opts&hasRoom != 0 {
			// If the room's not found, req.room will be null in the target
			// handler. It's the handler's responsibility to throw an error,
			// API or HTML response.
			room, err := app.hub.ActivateRoom(roomID)
			if err == nil {
				req.room = room
			}
		}

		// Attach the request context.
		ctx := context.WithValue(r.Context(), "ctx", req)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// readJSONReq reads the JSON body from a request and unmarshals it to the given target.
func readJSONReq(r *http.Request, o interface{}) error {
	defer r.Body.Close()
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, o)
}
