package mesos

import (
	"encoding/json"

	api "github.com/AVENTER-UG/mesos-compose/api"
	mesosutil "github.com/AVENTER-UG/mesos-util"
	"github.com/sirupsen/logrus"
)

func Heartbeat() {
	keys := api.GetAllRedisKeys("*")
	suppress := true
	for keys.Next(config.RedisCTX) {
		// get the values of the current key
		key := api.GetRedisKey(keys.Val())

		var task mesosutil.Command
		json.Unmarshal([]byte(key), &task)

		if task.TaskID == "" {
			continue
		}

		if task.State == "" {
			framework.CommandChan <- task

			task.State = "__NEW"
			data, _ := json.Marshal(task)
			err := config.RedisClient.Set(config.RedisCTX, task.TaskName+":"+task.TaskID, data, 0).Err()
			if err != nil {
				logrus.Error("HandleUpdate Redis set Error: ", err)
			}

			logrus.Info("Scheduled Mesos Task: ", task.TaskName)
		}

		if task.State == "__NEW" {
			mesosutil.Revive()
			suppress = false
		}
	}

	if suppress {
		mesosutil.SuppressFramework()
	}
}
