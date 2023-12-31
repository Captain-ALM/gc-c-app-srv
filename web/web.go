package web

import (
	"github.com/gorilla/mux"
	"golang.local/app-srv/conf"
	"log"
	"net/http"
)

func New(yaml conf.ConfigYaml) (*http.Server, *mux.Router) {
	router := mux.NewRouter()
	router.PathPrefix("/").HandlerFunc(domainNotAllowed)
	if yaml.Listen.Web == "" {
		log.Fatalf("[Http] Invalid Listening Address")
	}
	s := &http.Server{
		Addr:         yaml.Listen.Web,
		Handler:      router,
		ReadTimeout:  yaml.Listen.GetReadTimeout(),
		WriteTimeout: yaml.Listen.GetWriteTimeout(),
	}
	go runBackgroundHttp(s)
	return s, router
}

func runBackgroundHttp(s *http.Server) {
	err := s.ListenAndServe()
	if err != nil {
		if err == http.ErrServerClosed {
			log.Println("[Http] The http server shutdown successfully")
		} else {
			log.Fatalf("[Http] Error trying to host the http server: %s\n", err.Error())
		}
	}
}

func domainNotAllowed(rw http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusNotFound, "Domain Not Allowed")
	} else {
		rw.Header().Set("Allow", http.MethodOptions+", "+http.MethodGet+", "+http.MethodHead)
		if req.Method == http.MethodOptions {
			WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusOK, "")
		} else {
			WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusMethodNotAllowed, "")
		}
	}
}

func pageNotFound(rw http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusNotFound, "Page Not Found")
	} else {
		rw.Header().Set("Allow", http.MethodOptions+", "+http.MethodGet+", "+http.MethodHead)
		if req.Method == http.MethodOptions {
			WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusOK, "")
		} else {
			WriteResponseHeaderCanWriteBody(req.Method, rw, http.StatusMethodNotAllowed, "")
		}
	}
}
