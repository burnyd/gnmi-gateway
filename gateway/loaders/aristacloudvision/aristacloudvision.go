// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aristacloudvision

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/gnxi/utils/xpath"
	"github.com/openconfig/gnmi-gateway/gateway/configuration"
	"github.com/openconfig/gnmi-gateway/gateway/connections"
	"github.com/openconfig/gnmi-gateway/gateway/loaders"
	"github.com/openconfig/gnmi/proto/gnmi"
	targetpb "github.com/openconfig/gnmi/proto/target"
	"github.com/openconfig/gnmi/target"
)

var _ loaders.TargetLoader = new(CloudVisionLoader)

type CvPDevices struct {
	Result struct {
		Value struct {
			Key struct {
				DeviceID string `json:"deviceId"`
			} `json:"key"`
			Status    string `json:"status"`
			ZtpMode   bool   `json:"ztpMode"`
			IPAddress struct {
				Value string `json:"value"`
			} `json:"ipAddress"`
			ProvisioningGroupName string `json:"provisioningGroupName"`
		} `json:"value"`
		Time time.Time `json:"time"`
		Type string    `json:"type"`
	} `json:"result"`
}

type CloudVisionLoader struct {
	config     *configuration.GatewayConfig
	last       *targetpb.Configuration
	apiKey     string
	host       string
	interval   time.Duration
	targetport int
}

func init() {
	loaders.Register("Cloudvision", NewCloudvisionTargetLoader)
}

func NewCloudvisionTargetLoader(config *configuration.GatewayConfig) loaders.TargetLoader {
	return &CloudVisionLoader{
		config:     config,
		apiKey:     config.TargetLoaders.ClouVisionAPIKEY,
		host:       config.TargetLoaders.CloudVisionPortalHost,
		targetport: config.TargetLoaders.CloudVisionTargetPort,
	}
}

func (m *CloudVisionLoader) GetConfiguration() (*targetpb.Configuration, error) {
	var bearer = "Bearer " + m.apiKey
	req, err := http.NewRequest("GET", "https://"+m.host+"/api/resources/inventory/v1/ProvisionedDevice/all", nil)
	if err != nil {
		fmt.Errorf("Cannot connect to '%s'", m.host)
	}
	req.Header.Add("Authorization", bearer)
	req.Header.Add("Accept", "application/json")
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{Transport: tr}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Errorf("Cannot connect to CloudVision: %s", err)
	}
	defer resp.Body.Close()

	responseData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Errorf("Cannot marshall data", err)
	}

	configs := &targetpb.Configuration{
		Target:  make(map[string]*targetpb.Target),
		Request: make(map[string]*gnmi.SubscribeRequest),
	}

	var subs []*gnmi.Subscription
	for _, x := range m.config.TargetLoaders.CloudVisionSubscribePaths {
		path, err := xpath.ToGNMIPath(x)
		if err != nil {
			return nil, fmt.Errorf("unable to parse simple config XPath: %s: %v", x, err)
		}
		subs = append(subs, &gnmi.Subscription{Path: path})
	}
	configs.Request["default"] = &gnmi.SubscribeRequest{
		Request: &gnmi.SubscribeRequest_Subscribe{
			Subscribe: &gnmi.SubscriptionList{
				Prefix:       &gnmi.Path{},
				Subscription: subs,
			},
		},
	}
	//Since the API returns non json. ie it will return devices without a comma. I need to split every new line.
	SplitDevs := strings.Split(string(responseData), "\n")
	// Create a map of devices.
	devs := map[string]string{}
	//Loop through and add devices to devs map that are currently streaming.
	for _, i := range SplitDevs {
		var dev CvPDevices
		_ = json.Unmarshal([]byte(i), &dev)
		devs[dev.Result.Value.Key.DeviceID] = dev.Result.Value.IPAddress.Value + strconv.Itoa(m.targetport)
	}

	for dev, devip := range devs {
		if len(dev) > 0 {
			configs.Target[dev] = &targetpb.Target{
				Addresses: []string{devip},
				Request:   "default",
				Credentials: &targetpb.Credentials{
					Username: m.config.TargetLoaders.CloudVisionTargetUsername,
					Password: m.config.TargetLoaders.CloudVisionTargetPassword,
				},
			}
		}
	}
	if err := target.Validate(configs); err != nil {
		return nil, fmt.Errorf("configuration from CVP: %w", err)
	}
	return configs, nil

}

func (m *CloudVisionLoader) Start() error {

	_, err := m.GetConfiguration()
	return err
}

func (m *CloudVisionLoader) WatchConfiguration(targetChan chan<- *connections.TargetConnectionControl) error {
	for {
		targetConfig, err := m.GetConfiguration()
		if err != nil {
			m.config.Log.Error().Err(err).Msgf("Unable to get target configuration.")
		} else {
			controlMsg := new(connections.TargetConnectionControl)
			if m.last != nil {
				for targetName := range m.last.Target {
					_, exists := targetConfig.Target[targetName]
					if !exists {
						controlMsg.Remove = append(controlMsg.Remove, targetName)
					}
				}
			}
			controlMsg.Insert = targetConfig
			m.last = targetConfig

			targetChan <- controlMsg
		}
		time.Sleep(m.interval)
	}
}
