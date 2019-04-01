package producer

import (
	"fmt"
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
	prod.connectionString = conf.GetString("connectionString", "amqp://guest:guest@localhost:5672/")
	prod.enableDebugLog = conf.GetBool("debugLog", true)
	prod.exchangeName = conf.GetString("exchangeName","")
	prod.queueName = conf.GetString("queueName","")
	prod.ampqConsumerId = fmt.Sprintf("%s_%s", "gollum_rabbidAmpq_producer", conf.GetID())
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