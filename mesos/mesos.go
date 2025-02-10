package mesos

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	mesosproto "github.com/AVENTER-UG/mesos-compose/proto"
	cfg "github.com/AVENTER-UG/mesos-compose/types"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
)

// Mesos include all the current vars and global config
type Mesos struct {
	Config     *cfg.Config
	Framework  *cfg.FrameworkConfig
	IsSuppress bool
	IsRevive   bool
	CountAgent int
	Req        *http.Request
	Client     *http.Client
}

// Marshaler to serialize Protobuf Message to JSON
var marshaller = protojson.MarshalOptions{
	UseEnumNumbers: false,
	Indent:         " ",
	UseProtoNames:  true,
}

// New will create a new API object
func New(cfg *cfg.Config, frm *cfg.FrameworkConfig) *Mesos {
	e := &Mesos{
		Config:     cfg,
		Framework:  frm,
		IsSuppress: false,
		IsRevive:   false,
	}

	return e
}

func (e *Mesos) Subscribe() {
	subscribeCall := &mesosproto.Call{
		FrameworkId: e.Framework.FrameworkInfo.Id,
		Type:        mesosproto.Call_SUBSCRIBE.Enum(),
		Subscribe: &mesosproto.Call_Subscribe{
			FrameworkInfo: &e.Framework.FrameworkInfo,
		},
	}

	logrus.WithField("func", "scheduler.Subscribe").Debug(subscribeCall)
	body, _ := marshaller.Marshal(subscribeCall)
	client := &http.Client{}
	// #nosec G402
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: e.Config.SkipSSL},
	}

	protocol := "https"
	if !e.Framework.MesosSSL {
		protocol = "http"
	}
	req, _ := http.NewRequest("POST", protocol+"://"+e.Framework.MesosMasterServer+"/api/v1/scheduler", bytes.NewBuffer([]byte(body)))
	req.Close = true
	req.SetBasicAuth(e.Framework.Username, e.Framework.Password)
	req.Header.Set("Content-Type", "application/json")

	e.Req = req
	e.Client = client
}

// Revive will revive the mesos tasks to clean up
func (e *Mesos) Revive() {
	if !e.IsRevive {
		logrus.WithField("func", "mesos.Revive").Info("Framework Revive")
		e.IsSuppress = false
		e.IsRevive = true
		revive := &mesosproto.Call{
			Type: mesosproto.Call_REVIVE.Enum(),
		}
		err := e.Call(revive)
		if err != nil {
			logrus.WithField("func", "mesos.Revive").Error("Call Revive: ", err)
		}
	}
}

// ForceSuppressFramework if all Tasks are running, suppress framework offers
func (e *Mesos) ForceSuppressFramework() {
	e.IsSuppress = false
	e.SuppressFramework()
}

// SuppressFramework if all Tasks are running, suppress framework offers
func (e *Mesos) SuppressFramework() {
	if !e.IsSuppress {
		logrus.WithField("func", "mesos.SuppressFramework").Info("Framework Suppress")
		e.IsSuppress = true
		e.IsRevive = false
		suppress := &mesosproto.Call{
			Type: mesosproto.Call_SUPPRESS.Enum(),
		}
		err := e.Call(suppress)
		if err != nil {
			logrus.WithField("func", "mesos.SupressFramework").Error("Suppress Framework Call: ")
		}
	}
}

// Kill a Task with the given taskID
func (e *Mesos) Kill(taskID string, agentID string) error {
	logrus.WithField("func", "mesos.Kill").Info("Kill task ", taskID)
	// tell mesos to shutdonw the given task
	err := e.Call(&mesosproto.Call{
		Type: mesosproto.Call_KILL.Enum(),
		Kill: &mesosproto.Call_Kill{
			TaskId: &mesosproto.TaskID{
				Value: &taskID,
			},
			AgentId: &mesosproto.AgentID{
				Value: &agentID,
			},
		},
	})

	return err
}

// Call will send messages to mesos
func (e *Mesos) Call(message *mesosproto.Call) error {
	message.FrameworkId = e.Framework.FrameworkInfo.Id

	if message.GetType() == mesosproto.Call_ACKNOWLEDGE {
		if message.Acknowledge.GetUuid() == nil {
			return nil
		}
	}

	body, err := marshaller.Marshal(message)

	if err != nil {
		logrus.WithField("func", "mesos.Call").Debug("Could not Marshal message:", err.Error())
		return err
	}

	client := &http.Client{}
	// #nosec G402
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	protocol := "https"
	if !e.Framework.MesosSSL {
		protocol = "http"
	}
	req, _ := http.NewRequest("POST", protocol+"://"+e.Framework.MesosMasterServer+"/api/v1/scheduler", bytes.NewBuffer([]byte(body)))
	req.Close = true
	req.SetBasicAuth(e.Framework.Username, e.Framework.Password)
	req.Header.Set("Mesos-Stream-Id", e.Framework.MesosStreamID)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)

	if err != nil {
		logrus.WithField("func", "mesos.Call").Error("Call Message: ", err)
		return err
	}

	defer res.Body.Close()

	if res.StatusCode != 202 {
		body, err := io.ReadAll(res.Body)
		if err != nil {
			logrus.WithField("func", "mesos.Call").Error("Call Handling (could not read res.Body)")
			return fmt.Errorf("error %d", res.StatusCode)
		}

		logrus.WithField("func", "mesos.Call").Error("Call Handling: ", string(body))
	}

	return nil
}

// DecodeTask will decode the key into an mesos command struct
func (e *Mesos) DecodeTask(key string) *cfg.Command {
	var task *cfg.Command
	if key != "" {
		err := json.NewDecoder(strings.NewReader(key)).Decode(&task)
		if err != nil {
			logrus.WithField("func", "scheduler.DecodeTask").Error("Could not decode task: ", err.Error())
			return &cfg.Command{}
		}
		return task
	}
	return &cfg.Command{}
}

// GetOffer get out the offer for the mesos task
func (e *Mesos) GetOffer(offers *mesosproto.Event_Offers, cmd *cfg.Command) (*mesosproto.Offer, []*mesosproto.OfferID) {
	var offerIds []*mesosproto.OfferID
	var offerret *mesosproto.Offer

	for n, offer := range offers.Offers {
		logrus.Debug("Got Offer From:", offer.GetHostname())
		offerIds = append(offerIds, offer.Id)

		if cmd.TaskName == "" {
			continue
		}

		// if the ressources of this offer does not matched what the command need, the skip
		if !e.IsRessourceMatched(offer.Resources, cmd) {
			logrus.Debug("Could not found any matched ressources, get next offer")
			e.Call(e.DeclineOffer(offerIds))
			continue
		}
		offerret = offers.Offers[n]
	}
	return offerret, offerIds
}

// DeclineOffer will decline the given offers
func (e *Mesos) DeclineOffer(offerIds []*mesosproto.OfferID) *mesosproto.Call {

	logrus.WithField("func", "scheduler.HandleOffers").Debug("Offer Decline: ", offerIds)

	refuseSeconds := 120.0

	decline := &mesosproto.Call{
		Type: mesosproto.Call_DECLINE.Enum(),
		Decline: &mesosproto.Call_Decline{OfferIds: offerIds, Filters: &mesosproto.Filters{
			RefuseSeconds: &refuseSeconds,
		},
		},
	}
	return decline
}

// IsRessourceMatched - check if the ressources of the offer are matching the needs of the cmd
// nolint:gocyclo
func (e *Mesos) IsRessourceMatched(ressource []*mesosproto.Resource, cmd *cfg.Command) bool {
	mem := false
	cpu := false
	ports := true

	for _, v := range ressource {
		if v.GetName() == "cpus" && v.Scalar.GetValue() >= cmd.CPU {
			logrus.WithField("func", "mesos.IsRessourceMatched").Debug("Matched Offer CPU: ", cmd.CPU)
			cpu = true
		}
		if v.GetName() == "mem" && v.Scalar.GetValue() >= cmd.Memory {
			logrus.WithField("func", "mesos.IsRessourceMatched").Debug("Matched Offer Memory: ", cmd.Memory)
			mem = true
		}
		if len(cmd.DockerPortMappings) > 0 {
			if v.GetName() == "ports" {
				for _, taskPort := range cmd.DockerPortMappings {
					for _, portRange := range v.GetRanges().Range {
						portBegin := uint32(portRange.GetBegin())
						portEnd := uint32(portRange.GetEnd())
						if *taskPort.HostPort >= portBegin && *taskPort.HostPort <= portEnd {
							logrus.WithField("func", "mesos.IsRessourceMatched").Debug("Matched Offer TaskPort: ", taskPort.GetHostPort())
							logrus.WithField("func", "mesos.IsRessourceMatched").Debug("Matched Offer RangePort: ", portRange)
							ports = ports || true
							break
						}
						ports = ports || false
					}
				}
			}
		}
	}

	return mem && cpu && ports
}

// GetAgentInfo get information about the agent
func (e *Mesos) GetAgentInfo(agentID string) cfg.MesosSlaves {
	client := &http.Client{}
	// #nosec G402
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	protocol := "https"
	if !e.Framework.MesosSSL {
		protocol = "http"
	}
	req, _ := http.NewRequest("POST", protocol+"://"+e.Framework.MesosMasterServer+"/slaves/"+agentID, nil)
	req.Close = true
	req.SetBasicAuth(e.Framework.Username, e.Framework.Password)
	req.Header.Set("Mesos-Stream-Id", e.Framework.MesosStreamID)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)

	if err != nil {
		logrus.WithField("func", "mesos.getAgentInfo").Error("Could not connect to master: ", err.Error())
		return cfg.MesosSlaves{}
	}

	if res.StatusCode == http.StatusOK {
		defer res.Body.Close()

		var agent cfg.MesosAgent
		err = json.NewDecoder(res.Body).Decode(&agent)
		if err != nil {
			logrus.WithField("func", "getAgentInfo").Error("Could not encode json result: ", err.Error())
			// if there is an error, dump out the res.Body as debug
			bodyBytes, err := io.ReadAll(res.Body)
			if err == nil {
				logrus.WithField("func", "getAgentInfo").Debug("response Body Dump: ", string(bodyBytes))
			}
			return cfg.MesosSlaves{}
		}

		// save how many agents the cluster has
		e.CountAgent = len(agent.Slaves)

		// get the used agent info
		for _, a := range agent.Slaves {
			if a.ID == agentID {
				return a
			}
		}
	}

	return cfg.MesosSlaves{}
}

func (e *Mesos) GetNetworkInfo(taskID string) []*mesosproto.NetworkInfo {
	task := e.GetTaskInfo(taskID)

	if len(task.Tasks) > 0 {
		for _, status := range task.Tasks[0].Statuses {
			if status.State == "TASK_RUNNING" {
				var netw []*mesosproto.NetworkInfo
				netw = append(netw, status.ContainerStatus.NetworkInfos[0])
				return netw
			}
		}
	}
	return []*mesosproto.NetworkInfo{}
}

// GetTaskInfo get the task object to the given ID
func (e *Mesos) GetTaskInfo(taskID string) cfg.MesosTasks {
	client := &http.Client{}
	// #nosec G402
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	protocol := "https"
	if !e.Framework.MesosSSL {
		protocol = "http"
	}
	req, _ := http.NewRequest("POST", protocol+"://"+e.Framework.MesosMasterServer+"/tasks/?task_id="+taskID+"&framework_id="+e.Framework.FrameworkInfo.Id.GetValue(), nil)
	req.Close = true
	req.SetBasicAuth(e.Framework.Username, e.Framework.Password)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)

	if err != nil {
		logrus.WithField("func", "mesos.GetTaskInfo").Error("Could not connect to mesos-master: ", err.Error())
		return cfg.MesosTasks{}
	}

	defer res.Body.Close()

	var task cfg.MesosTasks
	err = json.NewDecoder(res.Body).Decode(&task)
	if err != nil {
		logrus.WithField("func", "mesos.GetTaskInfo").Error("Could not encode json result: ", err.Error())
		return cfg.MesosTasks{}
	}

	return task
}
