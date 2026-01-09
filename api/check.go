package api

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/sdslabs/beastv4/core"
	"github.com/sdslabs/beastv4/core/config"
	"github.com/sdslabs/beastv4/core/database"
	coreUtils "github.com/sdslabs/beastv4/core/utils"
	"github.com/sdslabs/beastv4/pkg/cr"
	"github.com/sdslabs/beastv4/pkg/remoteManager"
	"net/http"
	"strconv"
	"strings"
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

		localDeploy := challenge.ServerDeployed != core.LOCALHOST && challenge.ServerDeployed != ""

		exists, err := checkScriptScriptExists(localDeploy, challenge)
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "CONTAINER RUNTIME ERROR while processing the request.",
			})
			return
		}
		if !exists {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: fmt.Sprintf("VALIDATION ERROR: check.sh not found at %s.", core.SAD_CHECK_SCRIPT_LOCATION),
			})
			return
		}

		verified, err := validateCheckScriptHash(localDeploy, challenge)
		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "CONTAINER RUNTIME ERROR while processing the request.",
			})
			return
		}
		if !verified {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "VALIDATION ERROR: hash of check.sh does not match, file tampered with.",
			})
			return
		}

		result, err := executeCheckScript(localDeploy, challenge)
		solved = result.ExitCode == 0

		if err != nil {
			c.JSON(http.StatusInternalServerError, HTTPErrorResp{
				Error: "CONTAINER RUNTIME ERROR while processing the request.",
			})
			return
		}

		if !solved {
			c.JSON(http.StatusOK, FlagSubmitResp{
				Message: fmt.Sprintf("Challenge check failed with EXIT CODE: %v\nLOGS: %s", result.ExitCode, result.Output),
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

func checkScriptScriptExists(localDeploy bool, challenge database.Challenge) (bool, error) {
	var err error
	var result cr.ExecResult

	containerId := challenge.ContainerId
	fileCommand := fmt.Sprintf("[ -f '%s' ]", core.SAD_CHECK_SCRIPT_LOCATION)
	if localDeploy {
		result, err = cr.RunCommandInContainer(containerId, []string{
			"sh", "-c", fileCommand,
		})
	} else {
		server := config.Cfg.AvailableServers[challenge.ServerDeployed]
		result, err = remoteManager.RunCommandInContainerOnServer(server, containerId, fileCommand)
	}

	if err != nil || result.ExitCode != 0 {
		return false, err
	}

	return true, nil
}

func validateCheckScriptHash(localDeploy bool, challenge database.Challenge) (bool, error) {
	var err error
	var result cr.ExecResult

	containerId := challenge.ContainerId
	hashCommand := fmt.Sprintf("comand cat %s | sha256sum")
	if localDeploy {
		result, err = cr.RunCommandInContainer(containerId, []string{
			"sh", "-c", hashCommand,
		})
	} else {
		server := config.Cfg.AvailableServers[challenge.ServerDeployed]
		result, err = remoteManager.RunCommandInContainerOnServer(server, containerId, hashCommand)
	}

	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("check script hash failed")
	}

	return strings.TrimSpace(result.Output) == challenge.Flag, nil
}

func executeCheckScript(localDeploy bool, challenge database.Challenge) (cr.ExecResult, error) {
	if localDeploy {
		return cr.RunCommandInContainer(challenge.ContainerId, []string{
			"sh", "-c", core.SAD_CHECK_SCRIPT_LOCATION,
		})
	} else {
		server := config.Cfg.AvailableServers[challenge.ServerDeployed]
		return remoteManager.RunCommandInContainerOnServer(server, challenge.ContainerId, core.SAD_CHECK_SCRIPT_LOCATION)
	}
}
