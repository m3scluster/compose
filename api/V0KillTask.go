package api

import (
	"net/http"

	mesosutil "github.com/AVENTER-UG/mesos-util"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// V0KillTask will kill the given task id
// example:
// curl -X GET http://user:password@127.0.0.1:10000/v0/task/kill/{task id} -d 'JSON'
func V0KillTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	auth := CheckAuth(r, w)

	if vars == nil || !auth {
		return
	}

	d := []byte("nok")

	if vars["id"] != "" {
		id := vars["id"]
		ret := mesosutil.Kill(id)

		logrus.Error("V0TaskKill: ", ret)

		d = []byte("ok")
	}

	logrus.Debug("HTTP GET V0TaskKill: ", string(d))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Api-Service", "v0")

	w.Write(d)
}
