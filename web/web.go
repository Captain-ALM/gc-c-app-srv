package web

import (
	"errors"
	"github.com/gorilla/mux"
	"golang.local/app-srv/conf"
	"log"
	"net/http"
	"os"
	"strconv"
)

func New(yaml conf.ConfigYaml) (*http.Server, *mux.Router) {
	router := mux.NewRouter()
	if yaml.Listen.Web == "" {
		log.Fatalf("[Http] Invalid Listening Address")
	}
	s := &http.Server{
		Addr:         yaml.Listen.Web,
		Handler:      router,
		ReadTimeout:  yaml.Listen.GetReadTimeout(),
		WriteTimeout: yaml.Listen.GetWriteTimeout(),
	}
	if os.Getenv("LOG_REQUEST_METADATA") == "1" {
		router.Use(debugMiddleware)
	}
	router.Use(requestLimitMiddlewareGetter(yaml.Listen.GetReadLimit()))
	go runBackgroundHttp(s)
	return s, router
}

func runBackgroundHttp(s *http.Server) {
	err := s.ListenAndServe()
	if err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			log.Println("[Http] The http server shutdown successfully")
		} else {
			log.Fatalf("[Http] Error trying to host the http server: %s\n", err.Error())
		}
	}
}

func DomainNotAllowed(rw http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusOK, "")
	} else {
		rw.Header().Set("Allow", http.MethodOptions+", "+http.MethodGet+", "+http.MethodHead)
		if req.Method == http.MethodOptions {
			WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusOK, "")
		} else {
			WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusMethodNotAllowed, "")
		}
	}
}

func debugMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		DebugPrintln("REQ: " + r.Method + " ~ " + r.Host + " ~ " + r.RequestURI + " ~ " + strconv.Itoa(int(r.ContentLength)) + " ~ " + r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

func requestLimitMiddlewareGetter(rqLim int64) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > rqLim {
				w.WriteHeader(http.StatusExpectationFailed)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, rqLim)
			next.ServeHTTP(w, r)
		})
	}
}

func DebugPrintln(msg string) {
	if os.Getenv("DEBUG") == "1" {
		log.Println("DEBUG:", msg)
	}
}
