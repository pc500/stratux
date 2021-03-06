package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/takama/daemon"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// #include <wiringPi.h>
// #cgo LDFLAGS: -lwiringPi
import "C"

const (
	// CPU temperature target, degrees C
	defaultTempTarget = 50.
	hysteresis        = float32(1.)

	pwmClockDivisor = 100

	/* Minimum duty cycle is the point below which the fan does
	/* not spin. This depends on both your fan and the switching
	/* transistor used. */
	defaultPwmDutyMin = 1
	pwmDutyMax        = 10

	// how often to update
	delaySeconds = 30

	// GPIO-1/BCM "18"/Pin 12 on a Raspberry PI 3
	defaultPin = 1

	// name of the service
	name        = "fancontrol"
	description = "cooling fan speed control based on CPU temperature"

	// Address on which daemon should be listen.
	addr = ":9977"
)

type FanControl struct {
	TempTarget     float32
	TempCurrent    float32
	PWMDutyMin     int
	PWMDutyMax     int
	PWMDutyCurrent int
	PWMPin         int
}

var myFanControl FanControl

var stdlog, errlog *log.Logger

func fanControl() {
	cPin := C.int(myFanControl.PWMPin)

	C.wiringPiSetup()

	// Power on "test". Allows the user to verify that their fan is working.
	C.pinMode(cPin, C.OUTPUT)
	C.digitalWrite(cPin, C.HIGH)
	time.Sleep(5 * time.Second)
	C.digitalWrite(cPin, C.LOW)

	C.pwmSetMode(C.PWM_MODE_MS)
	C.pinMode(cPin, C.PWM_OUTPUT)
	C.pwmSetRange(C.uint(myFanControl.PWMDutyMax))
	C.pwmSetClock(pwmClockDivisor)
	C.pwmWrite(cPin, C.int(myFanControl.PWMDutyMin))
	myFanControl.TempCurrent = 0
	go cpuTempMonitor(func(cpuTemp float32) {
		if isCPUTempValid(cpuTemp) {
			myFanControl.TempCurrent = cpuTemp
		}
	})

	myFanControl.PWMDutyCurrent = 0

	delay := time.NewTicker(delaySeconds * time.Second)

	for {
		if myFanControl.TempCurrent > (myFanControl.TempTarget + hysteresis) {
			myFanControl.PWMDutyCurrent = iMax(iMin(myFanControl.PWMDutyMax, myFanControl.PWMDutyCurrent+1), myFanControl.PWMDutyMin)
		} else if myFanControl.TempCurrent < (myFanControl.TempTarget - hysteresis) {
			myFanControl.PWMDutyCurrent = iMax(myFanControl.PWMDutyCurrent-1, 0)
			if myFanControl.PWMDutyCurrent < myFanControl.PWMDutyMin {
				myFanControl.PWMDutyCurrent = 0
			}
		}
		//log.Println(myFanControl.TempCurrent, " ", myFanControl.PWMDutyCurrent)
		C.pwmWrite(cPin, C.int(myFanControl.PWMDutyCurrent))
		<-delay.C
	}

	// Default to "ON".
	C.pinMode(cPin, C.OUTPUT)
	C.digitalWrite(cPin, C.HIGH)
}

// Service has embedded daemon
type Service struct {
	daemon.Daemon
}

// Manage by daemon commands or run the daemon
func (service *Service) Manage() (string, error) {

	tempTarget := flag.Float64("temp", defaultTempTarget, "Target CPU Temperature, degrees C")
	pwmDutyMin := flag.Int("minduty", defaultPwmDutyMin, "Minimum PWM duty cycle")
	pin := flag.Int("pin", defaultPin, "PWM pin (wiringPi numbering)")
	flag.Parse()

	usage := "Usage: " + name + " install | remove | start | stop | status"
	// if received any kind of command, do it
	if flag.NArg() > 0 {
		command := os.Args[flag.NFlag()+1]
		switch command {
		case "install":
			return service.Install()
		case "remove":
			return service.Remove()
		case "start":
			return service.Start()
		case "stop":
			return service.Stop()
		case "status":
			return service.Status()
		default:
			return usage, nil
		}
	}

	myFanControl.TempTarget = float32(*tempTarget)
	myFanControl.PWMDutyMin = *pwmDutyMin
	myFanControl.PWMDutyMax = pwmDutyMax
	myFanControl.PWMPin = *pin

	go fanControl()

	// Set up channel on which to send signal notifications.
	// We must use a buffered channel or risk missing the signal
	// if we're not ready to receive when the signal is sent.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, os.Kill, syscall.SIGTERM)

	http.HandleFunc("/", handleStatusRequest)
	http.ListenAndServe(addr, nil)

	// interrupt by system signal
	for {
		killSignal := <-interrupt
		stdlog.Println("Got signal:", killSignal)
		if killSignal == os.Interrupt {
			return "Daemon was interrupted by system signal", nil
		}
		return "Daemon was killed", nil
	}
}

func handleStatusRequest(w http.ResponseWriter, r *http.Request) {
	statusJSON, _ := json.Marshal(&myFanControl)
	w.Write(statusJSON)
}

func init() {
	stdlog = log.New(os.Stdout, "", 0)
	errlog = log.New(os.Stderr, "", 0)
}

func main() {
	srv, err := daemon.New(name, description, []string{}...)
	if err != nil {
		errlog.Println("Error: ", err)
		os.Exit(1)
	}
	service := &Service{srv}
	status, err := service.Manage()
	if err != nil {
		errlog.Println(status, "\nError: ", err)
		os.Exit(1)
	}
	fmt.Println(status)
}
