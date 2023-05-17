package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

	"github.com/fsnotify/fsnotify"

	"github.com/gtuk/discordwebhook"
)

type Data struct {
	Level       string      `json:"level"`
	Ts          float64     `json:"ts"`
	Logger      string      `json:"logger"`
	Msg         string      `json:"msg"`
	Request     Request     `json:"request"`
	UserID      string      `json:"user_id"`
	Duration    float64     `json:"duration"`
	Size        int         `json:"size"`
	Status      int         `json:"status"`
	RespHeaders RespHeaders `json:"resp_headers"`
}

type Request struct {
	RemoteIP   string  `json:"remote_ip"`
	RemotePort string  `json:"remote_port"`
	Proto      string  `json:"proto"`
	Method     string  `json:"method"`
	Host       string  `json:"host"`
	URI        string  `json:"uri"`
	Headers    Headers `json:"headers"`
}

type Headers struct {
	AcceptEncoding  []string `json:"Accept-Encoding"`
	XForwardedFor   []string `json:"X-Forwarded-For"`
	CfRay           []string `json:"Cf-Ray"`
	XForwardedProto []string `json:"X-Forwarded-Proto"`
	CfVisitor       []string `json:"Cf-Visitor"`
	Accept          []string `json:"Accept"`
	Referer         []string `json:"Referer"`
	CfIpcountry     []string `json:"Cf-Ipcountry"`
	CdnLoop         []string `json:"Cdn-Loop"`
	UserAgent       []string `json:"User-Agent"`
	CfConnectingIP  []string `json:"Cf-Connecting-Ip"`
}

type RespHeaders struct {
	ContentLength []string `json:"Content-Length"`
	Server        []string `json:"Server"`
	AltSvc        []string `json:"Alt-Svc"`
	Etag          []string `json:"Etag"`
	ContentType   []string `json:"Content-Type"`
	LastModified  []string `json:"Last-Modified"`
	AcceptRanges  []string `json:"Accept-Ranges"`
}

type Config struct {
	ContainerName string `json:"containerName"`
	WebhookURL    string `json:"webhookUrl"`
	LogDir        string `json:"logDir"`
}

func getContainerIDByName(containerName string) (string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return "", err
	}

	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return "", err
	}

	for _, container := range containers {
		for _, name := range container.Names {
			if name == "/"+containerName {
				return container.ID, nil
			}
		}
	}

	return "", fmt.Errorf("container with name %s not found", containerName)
}

func executeCommandOnContainer(containerID string, cmd []string) (string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return "", err
	}

	ctx := context.Background()

	// Create a command to be executed within the container
	execResp, err := cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		WorkingDir:   "/var/log/caddy/",
	})
	if err != nil {
		return "", err
	}

	// Start the command execution
	execStartResp, err := cli.ContainerExecAttach(ctx, execResp.ID, types.ExecStartCheck{})
	if err != nil {
		return "", err
	}
	defer execStartResp.Close()

	// Read the output of the command
	var output strings.Builder
	_, err = io.Copy(&output, execStartResp.Reader)
	if err != nil {
		return "", err
	}

	// Get the exit status of the command
	execInspectResp, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", err
	}

	if execInspectResp.ExitCode != 0 {
		errMsg := fmt.Sprintf("Command execution failed with exit code %d", execInspectResp.ExitCode)
		log.Printf(errMsg)
		return "", errors.New(errMsg)
	}

	return output.String(), nil
}

func watchContainerFileChanges(targetPath string, webhookURL string, containerID string) {
	// Create an fsnotify watcher to monitor the target file or directory
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("Modified file:", event.Name)
					// get the file content
					fileContent, err := executeCommandOnContainer(containerID, []string{"cat", "access.log"})
					if err != nil {
						log.Println(err)
					}

					handleRequest(fileContent, webhookURL)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("Error watching files:", err)
			}
		}
	}()

	// Start the fsnotify watcher on the target file or directory
	err = watcher.Add(targetPath)
	if err != nil {
		log.Fatal(err)
	}

	<-done
}

var lastMessageContent string

func sendMessageToDiscord(content string, webhookUrl string) error {

	if content == lastMessageContent {
		// Skip sending the message if it's the same as the previous one
		log.Println("Skipping duplicate message to Discord:", content)
		return nil
	}

	message := discordwebhook.Message{

		Content: &content,
	}

	err := discordwebhook.SendMessage(webhookUrl, message)
	if err != nil {
		log.Fatal(err)
	}

	lastMessageContent = content

	return nil

}

func handleRequest(jsonString string, webhookUrl string) {

	// split the string into an array of strings based on \n
	var lines []string = strings.Split(jsonString, "\n")

	// get the last line of the array
	var lastLine string = lines[len(lines)-2]

	println(lastLine)

	// remove all error characters like "\x01"
	lastLine = strings.ReplaceAll(lastLine, "\x01", "")
	lastLine = strings.ReplaceAll(lastLine, "\x00", "")
	lastLine = strings.ReplaceAll(lastLine, "\x1e", "")

	var data Data
	err := json.Unmarshal([]byte(lastLine), &data)
	if err != nil {
		log.Println("JSON parse error:", err)
	}

	var date string = time.Unix(int64(data.Ts), 0).Format("2006-01-02 15:04:05")

	var importantInfo []string = []string{
		// strconv.FormatFloat(data.Ts, 'f', 4, 64),
		date,
		data.Request.Method,
		data.Request.Host,
		data.Request.Headers.CfConnectingIP[0],
		data.Request.Headers.UserAgent[0],
		fmt.Sprint(data.Status),
	}

	fmt.Println(importantInfo)

	// send message to discord webhook
	// [2023-05-17 13:03:52 GET imdb.simo.ng 50.230.198.1 Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/113.0.0.0 Safari/537.36 200]

	var messageContent string = "```" + importantInfo[0] + "\n---------------------------------------- \n" + importantInfo[2] + "\n" + importantInfo[3] + "\n" + importantInfo[4] + "\n" + importantInfo[5] + "```"

	sendMessageToDiscord(messageContent, webhookUrl)
}

func main() {

	filePath := "config.json"

	jsonData, err := ioutil.ReadFile(filePath)
	if err != nil {
		log.Fatal("Error reading JSON file:", err)
	}
	fmt.Println("Raw JSON data:")
	fmt.Println(string(jsonData))

	var config Config
	// convert string to json
	err2 := json.Unmarshal([]byte(string(jsonData)), &config)
	if err2 != nil {
		log.Println("JSON parse error:", err)
	}

	fmt.Println(config.ContainerName)

	// find container id based on container name
	containerName := config.ContainerName
	containerID, err := getContainerIDByName(containerName)

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(containerID)

	// executeCommandOnContainer(containerID, []string{"ls", "-l"})

	// w, _ := executeCommandOnContainer("f1a59be725c86d5abebeb93ab4e04eb2d4afca35e94f1c59204b5568a2a03adc", []string{"ls", "-l"})

	// fmt.Println(w)

	watchContainerFileChanges(config.LogDir, config.WebhookURL, containerID)
}
