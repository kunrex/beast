package cr

import (
	"github.com/sdslabs/beastv4/core"
	"github.com/sdslabs/beastv4/utils"
)

type localPort struct {
	challengeId uint
	port        uint32
}

var managedPorts []localPort

func checkPortAvailable(hostPort uint32) bool {
	for _, port := range managedPorts {
		if port.port == hostPort {
			return false
		}
	}

	return true
}

// RegisterPort
// Returns true if port wasn't is free and adds it to list of managed ports
func RegisterPort(challengeId uint, hostPort uint32) bool {
	if checkPortAvailable(hostPort) {
		return false
	}

	managedPorts = append(managedPorts, localPort{
		challengeId: challengeId,
		port:        hostPort,
	})
	return true
}

func GetAvailableSSHPort() (uint32, bool) {
	for i := core.SSHFanPortFirst; i < core.SSHFanPortLast; i++ {
		if !checkPortAvailable(i) {
			return i, true
		}
	}

	return 0, false
}

// FreeChallengePorts
// Frees all ports mapped to a container allowing other containers to use them
func FreeChallengePorts(challengeId uint) {
	var indices []int
	for i, port := range managedPorts {
		if port.challengeId == challengeId {
			indices = append(indices, i)
		}
	}

	for _, i := range indices {
		utils.RemoveElementAtIndexUnordered(managedPorts, i)
	}
}
