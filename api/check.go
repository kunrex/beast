package api

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/sdslabs/beastv4/core"
	"github.com/sdslabs/beastv4/core/config"
	"github.com/sdslabs/beastv4/core/database"
	coreUtils "github.com/sdslabs/beastv4/core/utils"
	"github.com/sdslabs/beastv4/pkg/cr"
	"github.com/sdslabs/beastv4/pkg/remoteManager"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strconv"
	"time"
)

func checkSolutionHandler(c *gin.Context) {
	challId := c.PostForm("chall_id")
	err, state := coreUtils.CheckTime()
	if err != nil {
		c.JSON(http.StatusBadRequest, HTTPErrorResp{
			Error: err.Error(),
		})
		return
	}

	if state == 0 {
		c.JSON(http.StatusBadRequest, HTTPErrorResp{
			Error: "Competition is yet to start",
		})
		return
	}
	if state == 2 {
		c.JSON(http.StatusBadRequest, HTTPErrorResp{
			Error: "Competition has ended",
		})
		return
	}

	if state == 1 {
		username, err := coreUtils.GetUser(c.GetHeader("Authorization"))
		if err != nil {
			c.JSON(http.StatusUnauthorized, HTTPErrorResp{
				Error: "Unauthorized user",
			})
			return
		}

		if challId == "" {
			c.JSON(http.StatusBadRequest, HTTPErrorResp{
				Error: "Id of the challenge is a required parameter to process request.",
			})
			return
		}

		user, err := database.QueryFirstUserEntry("username", username)
		if err != nil {
			c.JSON(http.StatusUnauthorized, HTTPErrorResp{
				Error: "Unauthorized user",
			})
			return
		}

		if user.Status == 1 {
			c.JSON(http.StatusUnauthorized, HTTPErrorResp{
				Error: "Banned user",
			})
			return
		}

		parsedChallId, err := strconv.Atoi(challId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "DATABASE ERROR while processing the request.",
			})
			return
		}

		chall, err := database.QueryChallengeEntries("id", strconv.Itoa(int(parsedChallId)))
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "DATABASE ERROR while processing the request.",
			})
			return
		}

		challenge := chall[0]
		if challenge.Status != core.DEPLOY_STATUS["deployed"] {
			c.JSON(http.StatusOK, FlagSubmitResp{
				Message: "Challenge is unavailable",
				Success: false,
			})
			return
		}

		if challenge.PreReqs != "" {
			preReqsStatus, err := database.CheckPreReqsStatus(challenge, user.ID)

			if err != nil {
				c.JSON(http.StatusInternalServerError, HTTPErrorResp{
					Error: "DATABASE ERROR while processing the request.",
				})
				return
			}

			if !preReqsStatus {
				c.JSON(http.StatusOK, FlagSubmitResp{
					Message: "You have not solved the prerequisites of this challenge.",
					Success: false,
				})
				return
			}
		}

		if challenge.MaxAttemptLimit > 0 {
			previousTries, err := database.GetUserPreviousTries(user.ID, challenge.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, HTTPErrorResp{
					Error: "DATABASE ERROR while processing the request."})
				return
			}

			if previousTries >= challenge.MaxAttemptLimit {
				c.JSON(http.StatusOK, FlagSubmitResp{
					Message: "You have reached the maximum number of tries for this challenge.",
					Success: false,
				})
				return
			}
		}

		// Increase user tries by 1
		err = database.UpdateUserChallengeTries(user.ID, challenge.ID)

		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "DATABASE ERROR while processing the request.",
			})
			return
		}
		solved, err := database.CheckPreviousSubmissions(user.ID, challenge.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "DATABASE ERROR while processing the request.",
			})
			return
		}

		if solved {
			c.JSON(http.StatusOK, FlagSubmitResp{
				Message: "Challenge has already been solved.",
				Success: false,
			})
			return
		}

		exitCode, err := executeCheckSolution(challenge)
		solved = exitCode == 0

		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "CONTAINER RUNTIME ERROR while processing the request.",
			})
			return
		}

		if !solved {
			c.JSON(http.StatusOK, FlagSubmitResp{
				Message: fmt.Sprintf("Challenge check failed with EXIT CODE: %v", exitCode),
				Success: false,
			})
			return
		}

		challengePoints := challenge.Points
		newScore := user.Score + challengePoints
		if newScore <= 0 {
			newScore = 0
		}
		err = database.UpdateUser(&user, map[string]interface{}{"Score": newScore})
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "DATABASE ERROR while processing the request.",
			})
			return
		}

		if len(adminLeaderboardCache) < core.LEADERBOARD_SIZE || (len(adminLeaderboardCache) > 0 && newScore > adminLeaderboardCache[len(adminLeaderboardCache)-1].Score) {
			leaderboardStale = true
			graphCacheStale = true
			adminLeaderboardStale = true
		}

		UserChallengesEntry := database.UserChallenges{
			CreatedAt:   time.Now(),
			UserID:      user.ID,
			ChallengeID: challenge.ID,
			Solved:      true,
			Flag:        "", // empty for now
		}

		err = database.SaveFlagSubmission(&UserChallengesEntry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "DATABASE ERROR while processing the request.",
			})
			return
		}

		c.JSON(http.StatusOK, FlagSubmitResp{
			Message: "Challenge check passed.",
			Success: true,
		})

		return
	}
}

func executeCheckSolution(challenge database.Challenge) (int, error) {
	challengeName := challenge.Name
	checkCommand := fmt.Sprintf("[ -f \"$HOME/check.sh\" ] && cd \"$HOME\" && ./check.sh")

	if challenge.ContainerId == coreUtils.GetTempContainerId(challengeName) {
		log.Warnf(fmt.Sprintf("No instance of challenge(%s) deployed", challengeName))
		return 1, errors.New("no instance of challenge")
	} else {
		log.Debugf("Checking check solution script for challenge(%s)", challengeName)
		if challenge.ServerDeployed != core.LOCALHOST && challenge.ServerDeployed != "" {
			server := config.Cfg.AvailableServers[challenge.ServerDeployed]
			return executeCheckSolutionOnRemoteContainer(challenge.ContainerId, checkCommand, server)
		} else {
			return executeCheckSolutionOnLocalhost(challenge.ContainerId, checkCommand)
		}
	}
}

func executeCheckSolutionOnLocalhost(containerId string, checkCommand string) (int, error) {
	containers, err := cr.SearchRunningContainerByFilter(map[string]string{"id": containerId})
	if err != nil {
		log.Errorf("error while searching for local container with id %s", containerId)
		return 1, errors.New("CONTAINER RUNTIME ERROR")
	}

	switch len(containers) {
	case 0:
		log.Error("Got no containers without throwing an error, something fishy here. Contact admin to check manually.")
		return 1, errors.New("CONTAINER RUNTIME ERROR")
	case 1:
		{
			result, err := cr.RunCommandInContainer(containerId, []string{
				"sh", "-c", checkCommand,
			})

			if err != nil {
				return 1, err
			}

			return result.ExitCode, err
		}
	default:
		log.Error("Got more than one containers, something fishy here. Contact admin to check manually.")
		return 1, errors.New("CONTAINER RUNTIME ERROR")
	}
}

func executeCheckSolutionOnRemoteContainer(containerId string, checkCommand string, server config.AvailableServer) (int, error) {
	remoteContainers, err := remoteManager.SearchRunningContainerByFilterRemote(map[string]string{"id": containerId}, server)
	if err != nil {
		log.Errorf("error while searching for remote container with id %s", containerId)
		return 1, errors.New("CONTAINER RUNTIME ERROR")
	}

	switch len(remoteContainers) {
	case 0:
		log.Error("Got no containers without throwing an error, something fishy here. Contact admin to check manually.")
		return 1, errors.New("CONTAINER RUNTIME ERROR")
	case 1:
		{
			dockerCommand := fmt.Sprintf("docker exec %s %s; echo $?", containerId, checkCommand)
			output, err := remoteManager.RunCommandOnServer(server, dockerCommand)
			if err != nil {
				return 1, err
			}

			converted, err := strconv.Atoi(output)
			if err == nil {
				return 1, err
			}

			return converted, nil
		}
	default:
		log.Error("Got more than one containers, something fishy here. Contact admin to check manually.")
		return 1, errors.New("CONTAINER RUNTIME ERROR")
	}
}
