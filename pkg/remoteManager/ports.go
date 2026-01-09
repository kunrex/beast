package remoteManager

import (
	"github.com/sdslabs/beastv4/core"
	"github.com/sdslabs/beastv4/core/config"
	"github.com/sdslabs/beastv4/utils"
)

type remotePort struct {
	challengeId uint
	host        string
	port        uint32
}

var remotePorts []remotePort

func checkPortAvailable(host string, hostPort uint32) bool {
	for _, port := range remotePorts {
		if port.host == host && port.port == hostPort {
			return false
		}
	}

	return true
}

// RegisterPort
// Returns true if port is free and adds it to managed ports
func RegisterPort(server config.AvailableServer, challengeId uint, hostPort uint32) bool {
	if !checkPortAvailable(server.Host, hostPort) {
		return false
	}

	remotePorts = append(remotePorts, remotePort{
		challengeId: challengeId,
		host:        server.Host,
		port:        hostPort,
	})

	return true
}

func GetAvailableSSHPort(server config.AvailableServer) (uint32, bool) {
	for i := core.SSHFanPortFirst; i < core.SSHFanPortLast; i++ {
		if checkPortAvailable(server.Host, i) {
			return i, true
		}
	}

	return 0, false
}

// FreeChallengePorts
// Frees all ports mapped to a container allowing other containers to use them
func FreeChallengePorts(challengeId uint) {
	var indices []int
	for i, port := range remotePorts {
		if port.challengeId == challengeId {
			indices = append(indices, i)
		}
	}

	for _, i := range indices {
		utils.RemoveElementAtIndexUnordered(remotePorts, i)
	}
}
