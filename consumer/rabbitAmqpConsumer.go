package consumer

import (
	"fmt"
	"github.com/trivago/gollum/core"
	"github.com/streadway/amqp"
	"sync"
)

type RabbitAmqpConsumer struct {
	core.SimpleConsumer
	connectionString string
	enableDebugLog bool
	queueName string
	ampqConsumerId string
	amqpConnection *amqp.Connection
	amqpChannel *amqp.Channel

}

func init() {
	core.TypeRegistry.Register(RabbitAmqpConsumer{})
}

func (cons *RabbitAmqpConsumer) Configure(conf core.PluginConfigReader) {
	//get connection string, default to the amqp testing defaults.
	cons.connectionString = conf.GetString("connectionString", "amqp://guest:guest@localhost:5672/")
	cons.enableDebugLog = conf.GetBool("debugLog", true)
	cons.queueName = conf.GetString("queueName","")
	cons.ampqConsumerId = fmt.Sprintf("%s_%s", "gollum_rabbidAmpq_consumer", conf.GetID())
}

func(cons *RabbitAmqpConsumer) Consume(workers *sync.WaitGroup) {
	go cons.startConnection()
	cons.ControlLoop()
}

func(cons *RabbitAmqpConsumer) debugLog(msg string) {
	if cons.enableDebugLog {
		cons.SimpleConsumer.GetLogger().Debug(msg)
	}
}


func (cons *RabbitAmqpConsumer) logErrorAndDie(err error, msg string) {
	if err != nil {
		cons.SimpleConsumer.GetLogger().Error("%s: %s", msg, err)
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}

func(cons *RabbitAmqpConsumer) startConnection() {
	cons.debugLog("starting connection")
	var err error
	cons.amqpConnection, err = amqp.Dial(cons.connectionString)
	cons.logErrorAndDie(err, "Failed to connect to RabbitMQ")

	cons.debugLog("opening channel")
	cons.amqpChannel, err = cons.amqpConnection.Channel()
	cons.logErrorAndDie(err, "Failed to open a channel")

	 go cons.handleConsume( cons.amqpChannel.Consume(
		cons.queueName,
		cons.ampqConsumerId,
		false,  //auto-ack
		false,
		false,
		false,
		nil,
	))

}

func(cons *RabbitAmqpConsumer) handleConsume(deliveries <-chan amqp.Delivery, err error) {
	fmt.Println("waiting for rabbitmq deliveries...")
	for d := range deliveries {
		s := string(d.Body)
		cons.debugLog("messge="+s)
		cons.Enqueue(d.Body)
		d.Ack(false)
	}
}



