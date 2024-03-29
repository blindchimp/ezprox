// This program implements an app-specific network relay.
//
// This process is used to relay control connections and audio and video
// data streams between dwyco applications. It is used in situations
// where the two client applications cannot connect directly to each
// other.
//
// Normally, this process is invoked by something like xinetd at the
// request of a local server that is assisting in connecting two clients.
// This process allocates some sockets, and relays the port information
// to the requesting server (on socket 0, if invoked by xinetd.)
// The server then relays the port information to the clients.
// The clients then initiate connections directly to this relay, which
// sets up clear channels between the sockets, and simply shovels
// data between the sockets.
// Multiple connections are assumed, the first is a "control connection"
// which is treated specially. If there are any errors or timeouts on
// this connection, the entire relay process is terminated.
// Other subsequent connections are treated more leniently, since they
// are expected to come and go as a normal part of the client
// communications. This process terminates after 1 hour, as a safety
// measure.
//
// Dwyco, Inc.
// Tue Mar 29 12:28:46 MST 2016
//
package main

import (
"net"
"fmt"
"time"
"os"
"flag"
)

func die() {
	os.Exit(1)
}

var watchdog *time.Timer;

func startWatchdog(wd **time.Timer, d int) {
	if *wd == nil {
		*wd = time.AfterFunc(time.Duration(d) * time.Second, die)
	} else {
		(*wd).Reset(time.Duration(d) * time.Second)
	}
}

func rendevous(lsock net.Listener, c chan net.Conn) {
	conn, err := lsock.Accept()
	if err != nil {
		panic("accept fail")
	}

	c <- conn

}

// accept connections continuously
func rendevousCont(lsock net.Listener, c chan net.Conn) {
	for {
		conn, err := lsock.Accept()
		if err != nil {
			panic("accept fail")
		}
		c <- conn
	}
}

// shovel data from the rd connection to the write connection.
// on error, if dieHard is true, just quit the process.
// otherwise, just close the connections and terminate the goroutine
// 
// timeouts: if dieHard is true, a short timeout is used because
// we know the protocol on that set of connections is pinging at
// regular intervals.
// otherwise, just let the global watchdog run, and kill the
// process after an hour or so (this keeps situations where someone
// walks away and leaves video streaming, for example.)
func shovel(rd net.Conn, wr net.Conn, dieHard bool) {
	buf := make([]byte, 8192)
	var watchdog *time.Timer
	for {
		if dieHard {
			startWatchdog(&watchdog, 1200)
		}
		n, err := rd.Read(buf)
		if err != nil {
			if dieHard {
				os.Exit(0)
				panic(err.Error())
			} else {
				// note: we know there is another
				// go shovel using our same set of
				// connections, just in the opposite order,
				// so closing them in one routine will result
				// in errors in the other side, causing the
				// proxy connection to shutdown completely
				rd.Close()
				wr.Close()
				break;
			}
		}
		wbuf := buf[:n]
		n2, err := wr.Write(wbuf)
		if n != n2 {
			if dieHard {
				os.Exit(0)
				panic("oops")
			} else {
				rd.Close()
				wr.Close()
				break;
			}
		}
	}
}

func encodeLong(l int) string {
	str := fmt.Sprintf("%d", l)
	lenstr := len(str)

	outstr := fmt.Sprintf("%02d%d", lenstr, l)
	return outstr
}

// just bogus up something the existing servers can eat
// so we can test this in production
func fauxXferOut(callerAddr string, calleeAddr string) string {
	len1 := len(callerAddr)
	len2 := len(calleeAddr)

	// vector(s1 s2)
	return fmt.Sprintf("0901202%s%s02%s%s", encodeLong(len1), callerAddr,
		encodeLong(len2), calleeAddr)
}


func main() {
	startWatchdog(&watchdog, 3)
	var dir string
	flag.StringVar(&dir, "c", "/tmp", "directory to change to")
	flag.Parse()

	err := os.Chdir(dir)
	if err != nil {
		panic("cant change to dir")
	}

	var hostip string

	cfg, err := os.Open("cfg/HostIP")
	if err != nil {
		hostip = "127.0.0.1"
	} else {
		fmt.Fscanf(cfg, "%s", &hostip)
		//hostip = append(hostip, ":")
	}
	hostip += ":"


	callerSock, _ := net.Listen("tcp", hostip)
	calleeSock, _ := net.Listen("tcp", hostip)

	//fmt.Println(callerSock.Addr())
	//fmt.Println(calleeSock.Addr())

	proxyInfo := fauxXferOut(callerSock.Addr().String(), calleeSock.Addr().String())
	std := os.NewFile(0, "okdokey")

	infoConn, err := net.FileConn(std)
	if err != nil {
		panic(err.Error())
	}

	bpi := []byte(proxyInfo)

	infoConn.Write(bpi)
	infoConn.Close()
	std.Close()

	// increase to 2 minutes in case it is a glacial phone
	// trying to get connected.
	startWatchdog(&watchdog, 120)


	// note: the first rendevous is for a control
	// channel. if either end of that dies, we nuke
	// the process.
	var connChan chan net.Conn = make(chan net.Conn)
	//var calleeChan chan Conn = make(chan Conn)
	go rendevous(callerSock, connChan)
	go rendevous(calleeSock, connChan)

	// i don't think the ordering of caller/callee is important
	// at this point
	conn1 := <-connChan
	conn2 := <-connChan

	go shovel(conn1, conn2, true)
	go shovel(conn2, conn1, true)

	// these channels can come and go as new media is
	// negotiated via the control channel
	var c1Chan chan net.Conn = make(chan net.Conn, 4)
	var c2Chan chan net.Conn = make(chan net.Conn, 4)
	go rendevousCont(callerSock, c1Chan)
	go rendevousCont(calleeSock, c2Chan)

	// just completely die after an hour
	//watchdog.Stop()
	startWatchdog(&watchdog, 3600)
	for {
		// loop just pairing up connections as they come in.
		// if something happens where we get stuck with nothing in
		// one of the connection channels for a long period of time,
		// just time out since one side is probably in the process
		// of dieing
		//
		// i don't think the ordering of caller/callee is important
		// at this point, the clients negotiate which sessions go with what.
		var c1 net.Conn
		var c2 net.Conn
		select {
		case c1 = <-c1Chan :
			select {
				case c2 = <-c2Chan : {
				}
				case <- time.After(time.Second * 120) :
					c1.Close()
					continue
				}

		case c2 = <-c2Chan :
			select {
				case c1 = <-c1Chan : {
				}
				case <- time.After(time.Second * 120) :
					c2.Close()
					continue
				}

		case <- time.After(time.Second * 3) :
			continue
		}
		go shovel(c1, c2, false)
		go shovel(c2, c1, false)
	}
}
