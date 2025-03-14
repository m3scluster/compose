package scheduler

import (
	"bufio"
	"net/http"
	"strconv"
	"strings"

	api "github.com/AVENTER-UG/mesos-compose/api"
	"github.com/AVENTER-UG/mesos-compose/mesos"
	mesosproto "github.com/AVENTER-UG/mesos-compose/proto"
	"github.com/AVENTER-UG/mesos-compose/redis"
	cfg "github.com/AVENTER-UG/mesos-compose/types"
	"github.com/AVENTER-UG/util/vault"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
)

// Scheduler include all the current vars and global config
type Scheduler struct {
	Config          *cfg.Config
	Framework       *cfg.FrameworkConfig
	Mesos           mesos.Mesos
	Client          *http.Client
	Req             *http.Request
	API             *api.API
	Vault           *vault.Vault
	Redis           *redis.Redis
	ConnectionError bool
}

// Marshaler to serialize Protobuf Message to JSON
var marshaller = protojson.MarshalOptions{
	UseEnumNumbers: false,
	Indent:         " ",
	UseProtoNames:  true,
}

// Subscribe to the mesos backend
func Subscribe(cfg *cfg.Config, frm *cfg.FrameworkConfig) *Scheduler {
	e := &Scheduler{
		Config:    cfg,
		Framework: frm,
		Mesos:     *mesos.New(cfg, frm),
	}

	e.Mesos.Subscribe()

	return e
}

// EventLoop is the main loop for the mesos events.
func (e *Scheduler) EventLoop() {
	res, err := e.Mesos.Client.Do(e.Mesos.Req)

	if err != nil {
		logrus.WithField("func", "scheduler.EventLoop").Errorf("Mesos Master connection error: %s", err.Error())
		return
	}
	defer res.Body.Close()

	reader := bufio.NewReader(res.Body)

	line, err := reader.ReadString('\n')
	if err != nil {
		logrus.WithField("func", "scheduler.EventLoop").Errorf("Error read string from Mesos Master: %s", err.Error())
		return
	}
	bytesCount, err := strconv.Atoi(strings.Trim(line, "\n"))
	if err != nil {
		logrus.WithField("func", "scheduler.EventLoop").Errorf("Error get bytescount from string: %s", err.Error())
		return
	}

	for {
		// Read line from Mesos
		line, err = reader.ReadString('\n')
		if err != nil {
			logrus.WithField("func", "scheduler.EventLoop").Errorf("Error to read data from Mesos Master: %s", err.Error())
			return
		}
		line = strings.Trim(line, "\n")

		// skip if no data
		if line == "" || len(line)-1 < bytesCount {
			logrus.WithField("func", "scheduler.EventLoop").Tracef("Line %s, bytesCount: %d ", line, bytesCount)
			logrus.WithField("func", "scheduler.EventLoop").Trace("No data from Mesos Master")
			continue
		}
		data := line[:bytesCount]
		bytesCount, _ = strconv.Atoi(line[bytesCount:])

		// Read important data
		var event mesosproto.Event // Event as ProtoBuf
		err := protojson.Unmarshal([]byte(data), &event)
		if err != nil {
			logrus.WithField("func", "scheduler.EventLoop").Warnf("Could not unmarshal Mesos Master data: %s", err.Error())
			continue
		}

		logrus.WithField("func", "scheduler.EventLoop").Tracef("Event %s", event.GetType().String())

		switch event.Type.Number() {
		case mesosproto.Event_SUBSCRIBED.Number():
			logrus.WithField("func", "scheduler.EventLoop").Info("Subscribed")
			logrus.WithField("func", "scheduler.EventLoop").Debugf("FrameworkId: %s", event.Subscribed.GetFrameworkId())
			e.Framework.FrameworkInfo.Id = event.Subscribed.GetFrameworkId()
			e.Framework.MesosStreamID = res.Header.Get("Mesos-Stream-Id")

			if e.Config.ThreadEnable {
				go e.reconcile()
			} else {
				e.reconcile()
			}
			go e.Redis.SaveFrameworkRedis(e.Framework)
			go e.Redis.SaveConfig(*e.Config)
		case mesosproto.Event_UPDATE.Number():
			if e.Config.ThreadEnable {
				go e.HandleUpdate(&event)
			} else {
				e.HandleUpdate(&event)
			}
			go e.callPluginEvent(&event)
		case mesosproto.Event_HEARTBEAT.Number():
			if e.Framework.FrameworkInfo.Id != nil {
				if e.Framework.FrameworkInfo.Id.GetValue() == "" {
					logrus.WithField("func", "scheduler.EventLoop").Tracef("HEARBEAT: Could not find framework ID")
					return
				}
			}
		case mesosproto.Event_OFFERS.Number():
			// Search Failed containers and restart them
			err = e.HandleOffers(event.Offers)
			if err != nil {
				logrus.WithField("func", "scheduler.EventLoop").Warn("Switch Event HandleOffers: ", err)
			}
		}
	}
}

func (e *Scheduler) changeDockerPorts(cmd *cfg.Command) []*mesosproto.ContainerInfo_DockerInfo_PortMapping {
	var ret []*mesosproto.ContainerInfo_DockerInfo_PortMapping
	for _, port := range cmd.DockerPortMappings {
		port.HostPort = e.API.GetRandomHostPort()
		ret = append(ret, port)
	}
	return ret
}

func (e *Scheduler) changeDiscoveryInfo(cmd *cfg.Command) *mesosproto.DiscoveryInfo {
	for i, port := range cmd.DockerPortMappings {
		cmd.Discovery.Ports.Ports[i].Number = port.HostPort
	}
	return cmd.Discovery
}

// reconcile will ask Mesos about the current state of the given tasks
func (e *Scheduler) reconcile() {
	var oldTasks []*mesosproto.Call_Reconcile_Task
	keys := e.Redis.GetAllRedisKeys(e.Framework.FrameworkName + ":*")
	for keys.Next(e.Redis.CTX) {
		// continue if the key is not a mesos task
		if e.Redis.CheckIfNotTask(keys) {
			continue
		}

		keys.Val()

		key := e.Redis.GetRedisKey(keys.Val())

		task := e.Mesos.DecodeTask(key)

		if task.TaskID == "" || task.Agent == "" || task.State == "__NEW" || task.State == "__KILL" || task.State == "" {
			continue
		}

		oldTasks = append(oldTasks, &mesosproto.Call_Reconcile_Task{
			TaskId: &mesosproto.TaskID{
				Value: &task.TaskID,
			},
			AgentId: &mesosproto.AgentID{
				Value: &task.MesosAgent.ID,
			},
		})
		logrus.WithField("func", "mesos.Reconcile").Debug("Reconcile Task: ", task.TaskID)
	}
	err := e.Mesos.Call(&mesosproto.Call{
		Type:      mesosproto.Call_RECONCILE.Enum(),
		Reconcile: &mesosproto.Call_Reconcile{Tasks: oldTasks},
	})

	if err != nil {
		logrus.WithField("func", "scheduler.reconcile").Debug("Reconcile Error: ", err)
	}
}

// implicitReconcile will ask Mesos which tasks and there state are registert to this framework
func (e *Scheduler) implicitReconcile() {
	var noTasks []*mesosproto.Call_Reconcile_Task
	err := e.Mesos.Call(&mesosproto.Call{
		Type:      mesosproto.Call_RECONCILE.Enum(),
		Reconcile: &mesosproto.Call_Reconcile{Tasks: noTasks},
	})

	if err != nil {
		logrus.WithField("func", "scheduler.implicitReconcile").Debug("Reconcile Error: ", err)
	}
}

func (e *Scheduler) callPluginEvent(event *mesosproto.Event) {
	if e.Config.PluginsEnable {
		for _, p := range e.Config.Plugins {
			symbol, err := p.Lookup("Event")
			if err != nil {
				logrus.WithField("func", "scheduler.callPluginEvent").Error("Error lookup event function in plugin: ", err.Error())
				continue
			}

			eventPluginFunc, ok := symbol.(func(*mesosproto.Event))
			if !ok {
				logrus.WithField("func", "main.initPlugins").Error("Error plugin does not have init function")
				continue
			}

			eventPluginFunc(event)
		}
	}
}
