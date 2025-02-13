package redis

import (
	"context"
	"encoding/json"

	"github.com/AVENTER-UG/mesos-compose/mesos"
	mesosproto "github.com/AVENTER-UG/mesos-compose/proto"
	cfg "github.com/AVENTER-UG/mesos-compose/types"
	goredis "github.com/redis/go-redis/v9"

	"github.com/sirupsen/logrus"
)

// Redis struct about the redis connection
type Redis struct {
	Client   *goredis.Client
	CTX      context.Context
	Server   string
	Password string
	DB       int
	PoolSize int
	Prefix   string
	Mesos    mesos.Mesos
}

// New will create a new Redis object
func New(cfg *cfg.Config, frm *cfg.FrameworkConfig) *Redis {
	e := &Redis{
		Server:   cfg.RedisServer,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
		PoolSize: cfg.RedisPoolSize,
		Prefix:   frm.FrameworkName,
		CTX:      context.Background(),
		Mesos:    *mesos.New(cfg, frm),
	}

	return e
}

// GetAllRedisKeys get out all redis keys to a patter
func (e *Redis) GetAllRedisKeys(pattern string) *goredis.ScanIterator {
	val := e.Client.Scan(e.CTX, 0, pattern, 0).Iterator()
	if err := val.Err(); err != nil {
		logrus.WithField("func", "redis.GetAllRedisKeys").Error("Error getting all keys: ", err)
	}

	return val
}

// GetRedisKey get out the data of a key
func (e *Redis) GetRedisKey(key string) string {
	val, err := e.Client.Get(e.CTX, key).Result()
	if err != nil {
		logrus.WithField("func", "redis.GetRedisKey").Errorf("Error getting key (%s): %s", key, err.Error())
	}

	return val
}

// SetRedisKey store data in redis
func (e *Redis) SetRedisKey(data []byte, key string) {
	err := e.Client.Set(e.CTX, key, data, 0).Err()
	if err != nil {
		logrus.WithField("func", "SetRedisKey").Error("Could not save data in Redis: ", err.Error())
	}
}

// DelRedisKey will delete a redis key
func (e *Redis) DelRedisKey(key string) int64 {
	val, err := e.Client.Del(e.CTX, key).Result()
	if err != nil {
		logrus.WithField("func", "redis.DelRedisKey").Error("de.Key: ", err)
		e.PingRedis()
	}

	return val
}

// GetTaskFromEvent get out the key by a mesos event
func (e *Redis) GetTaskFromEvent(update *mesosproto.Event_Update) *cfg.Command {
	// search matched taskid in redis and update the status
	keys := e.GetAllRedisKeys(e.Prefix + ":*")
	for keys.Next(e.CTX) {
		// ignore redis keys if they are not mesos tasks
		if e.CheckIfNotTask(keys) {
			continue
		}
		// get the values of the current key
		key := e.GetRedisKey(keys.Val())
		task := e.Mesos.DecodeTask(key)

		if task.TaskID == update.Status.TaskId.GetValue() {
			task.State = update.Status.State.String()
			return task
		}
	}

	return &cfg.Command{}
}

// GeTaskFromTaskID get out the task by a taskID
func (e *Redis) GetTaskFromTaskID(taskID string) *cfg.Command {
	// search matched taskid in redis and update the status
	keys := e.GetAllRedisKeys(e.Prefix + ":*")
	for keys.Next(e.CTX) {
		// ignore redis keys if they are not mesos tasks
		if e.CheckIfNotTask(keys) {
			continue
		}
		// get the values of the current key
		key := e.GetRedisKey(keys.Val())
		task := e.Mesos.DecodeTask(key)

		if task.TaskID == taskID {
			return task
		}
	}

	return &cfg.Command{}
}

// CountRedisKey will get back the count of the redis key
func (e *Redis) CountRedisKey(pattern string, ignoreState string) int {
	keys := e.GetAllRedisKeys(pattern)
	count := 0
	for keys.Next(e.CTX) {
		// ignore redis keys if they are not mesos tasks
		if e.CheckIfNotTask(keys) {
			continue
		}

		// do not count these key
		if ignoreState != "" {
			// get the values of the current key
			key := e.GetRedisKey(keys.Val())
			task := e.Mesos.DecodeTask(key)

			if task.State == ignoreState {
				continue
			}
		}
		count++
	}
	return count
}

// CountRedisKeyState will get back the amount of redis keys with the spesific state.
func (e *Redis) CountRedisKeyState(pattern string, state string) int {
	keys := e.GetAllRedisKeys(pattern)
	count := 0
	for keys.Next(e.CTX) {
		// ignore redis keys if they are not mesos tasks
		if e.CheckIfNotTask(keys) {
			continue
		}

		// do not count these key
		if state != "" {
			// get the values of the current key
			key := e.GetRedisKey(keys.Val())
			task := e.Mesos.DecodeTask(key)

			if task.State != state {
				continue
			}
		}
		count++
	}
	return count
}

// SaveConfig store the current framework config
func (e *Redis) SaveConfig(config cfg.Config) {
	data, _ := json.Marshal(config)
	err := e.Client.Set(e.CTX, e.Prefix+":framework_config", data, 0).Err()
	if err != nil {
		logrus.WithField("func", "redis.SaveConfig").Error("Framework save config state into redis error:", err)
	}
}

// PingRedis to check the health of redis
func (e *Redis) PingRedis() error {
	pong, err := e.Client.Ping(e.CTX).Result()
	if err != nil {
		logrus.WithField("func", "PingRedis").Error("Did not pon Redis: ", pong, err.Error())
	}
	if err != nil {
		return err
	}
	return nil
}

// Connect will connect the redis DB and save the client pointer
func (e *Redis) Connect() bool {
	var redisOptions goredis.Options
	redisOptions.Addr = e.Server
	redisOptions.DB = e.DB
	redisOptions.PoolSize = e.PoolSize

	if e.Password != "" {
		redisOptions.Password = e.Password
	}

	e.Client = goredis.NewClient(&redisOptions)

	err := e.PingRedis()
	if err != nil {
		e.Client.Close()
		return false
	}

	return true
}

// SaveTaskRedis store mesos task in DB
func (e *Redis) SaveTaskRedis(cmd *cfg.Command) {
	if cmd.TaskName != "" {
		d, _ := json.Marshal(&cmd)
		e.SetRedisKey(d, cmd.TaskName+":"+cmd.TaskID)
	}
}

// SaveFrameworkRedis store mesos framework in DB
func (e *Redis) SaveFrameworkRedis(framework *cfg.FrameworkConfig) {
	d, _ := json.Marshal(&framework)
	e.SetRedisKey(d, e.Prefix+":framework")
}

// CheckIfNotTask check if the redis key is a mesos task
func (e *Redis) CheckIfNotTask(keys *goredis.ScanIterator) bool {
	if keys.Val() == e.Prefix+":framework" || keys.Val() == e.Prefix+":framework_config" {
		return true
	}
	return false
}
