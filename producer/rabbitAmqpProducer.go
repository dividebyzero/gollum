package producer

import (
	"fmt"
	"net/url"
	"reflect"
	"sync"
	"github.com/trivago/gollum/core"
	"github.com/streadway/amqp"
)

type RabbitAmqpProducer struct {
	core.BufferedProducer
	connectionString string
	enableDebugLog bool
	exchangeName string
	queueName string
	ampqConsumerId string
	amqpConnection *amqp.Connection
	amqpChannel *amqp.Channel
}

func init() {
	core.TypeRegistry.Register(RabbitAmqpProducer{})
}

func (prod *RabbitAmqpProducer) Configure(conf core.PluginConfigReader) {
	//get connection string, default to the amqp testing defaults.
	prod.connectionString = prod.buildConnectionString(
		conf.GetString("user","guest"),
		conf.GetString("password","guest"),
		conf.GetString("host","localhost"),
		conf.GetString("port","5672"),
	)

	prod.enableDebugLog = conf.GetBool("debugLog", true)
	prod.exchangeName = conf.GetString("exchangeName","")
	prod.queueName = conf.GetString("queueName","")
	prod.ampqConsumerId = fmt.Sprintf("%s_%s", "gollum_rabbidAmpq_producer", conf.GetID())
}

func(cons *RabbitAmqpProducer) buildConnectionString(user string, pass string, host string, port string) string {
	var encodedUsr = url.UserPassword(user, pass).String()
	conurl, err := url.Parse(fmt.Sprintf(
		"amqp://%s@%s:%s",encodedUsr,host,port))
	cons.logErrorAndDie(err,"failed to build connection string")
	return conurl.String()
}

func (prod *RabbitAmqpProducer) pushMessage(msg *core.Message) {
	prod.debugLog("sending message")
	amqpMsg := amqp.Publishing {
		DeliveryMode: amqp.Persistent,
		Timestamp:    msg.GetCreationTime(),
		ContentType:  "text/plain",
		Body:        	 msg.GetPayload(),
	}
	if(prod.amqpChannel == nil){
		prod.debugLog("Channel is Null")
		return
	}
	prod.amqpChannel.Publish(prod.exchangeName,prod.queueName,false,false, amqpMsg)
}

// Produce writes to stdout or stderr.
func (prod *RabbitAmqpProducer) Produce(workers *sync.WaitGroup) {
	defer prod.WorkerDone()
	go prod.startConnection()
	prod.AddMainWorker(workers)
	prod.MessageControlLoop(prod.pushMessage)
}

func(prod *RabbitAmqpProducer) debugLog(msg string) {
	if prod.enableDebugLog {
		prod.BufferedProducer.GetLogger().Debug(msg)
	}
}


func (prod *RabbitAmqpProducer) logErrorAndDie(err error, msg string) {
	if err != nil {
		prod.BufferedProducer.GetLogger().Error("%s: %s", msg, err)
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}

func(prod *RabbitAmqpProducer) startConnection() {
	prod.debugLog("starting connection")
	var err error
	prod.amqpConnection, err = amqp.Dial(prod.connectionString)
	prod.logErrorAndDie(err, "Failed to connect to RabbitMQ")

	prod.debugLog("opening channel")
	prod.amqpChannel, err = prod.amqpConnection.Channel()
	prod.debugLog("channel ="+ reflect.TypeOf(prod.amqpChannel).String() );
	prod.logErrorAndDie(err, "Failed to open a channel")

}