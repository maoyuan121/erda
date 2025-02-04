// Copyright (c) 2021 Terminus, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package deployment_order

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/erda-project/erda/apistructs"
	"github.com/erda-project/erda/modules/orchestrator/dbclient"
	"github.com/erda-project/erda/modules/orchestrator/services/apierrors"
	"github.com/erda-project/erda/modules/orchestrator/utils"
	"github.com/erda-project/erda/modules/pkg/user"
)

func (d *DeploymentOrder) Create(req *apistructs.DeploymentOrderCreateRequest) (*apistructs.DeploymentOrderCreateResponse, error) {
	// generate order id
	if req.Id == "" {
		req.Id = uuid.NewString()
	}

	// get release info
	releaseResp, err := d.bdl.GetRelease(req.ReleaseId)
	if err != nil {
		logrus.Errorf("failed to get release %s, err: %v", req.ReleaseId, err)
		return nil, err
	}

	// permission check
	if err := d.batchCheckExecutePermission(req.Operator, req.Workspace, d.parseAppsInfoWithRelease(releaseResp)); err != nil {
		return nil, apierrors.ErrCreateDeploymentOrder.InternalError(err)
	}

	// parse the type of deployment order
	req.Type = parseOrderType(req.Type, releaseResp.IsProjectRelease)

	// compose deployment order
	order, err := d.composeDeploymentOrder(releaseResp, req)
	if err != nil {
		logrus.Errorf("failed to compose deployment order, error: %v", err)
		return nil, err
	}

	// save order to db
	if err := d.db.UpdateDeploymentOrder(order); err != nil {
		logrus.Errorf("failed to update deployment order, err: %v", err)
		return nil, err
	}

	createResp := &apistructs.DeploymentOrderCreateResponse{
		Id:              order.ID,
		Name:            utils.ParseOrderName(order.ID),
		Type:            order.Type,
		ReleaseId:       order.ReleaseId,
		ProjectId:       order.ProjectId,
		ProjectName:     order.ProjectName,
		ApplicationId:   order.ApplicationId,
		ApplicationName: order.ApplicationName,
		Status:          parseDeploymentOrderStatus(nil),
	}

	if req.AutoRun {
		executeDeployResp, err := d.executeDeploy(order, releaseResp)
		if err != nil {
			logrus.Errorf("failed to executeDeploy, err: %v", err)
			return nil, err
		}
		createResp.Deployments = executeDeployResp
	}

	return createResp, nil
}

func (d *DeploymentOrder) Deploy(req *apistructs.DeploymentOrderDeployRequest) (*dbclient.DeploymentOrder, error) {
	order, err := d.db.GetDeploymentOrder(req.DeploymentOrderId)
	if err != nil {
		logrus.Errorf("failed to get deployment order, err: %v", err)
		return nil, err
	}

	appsInfo, err := d.parseAppsInfoWithOrder(order)
	if err != nil {
		logrus.Errorf("failed to parse application info with order, err: %v", err)
		return nil, err
	}

	// permission check
	if err := d.batchCheckExecutePermission(req.Operator, order.Workspace, appsInfo); err != nil {
		logrus.Errorf("failed to check execute permission, err: %v", err)
		return nil, apierrors.ErrDeployDeploymentOrder.InternalError(err)
	}

	order.Operator = user.ID(req.Operator)

	releaseResp, err := d.bdl.GetRelease(order.ReleaseId)
	if err != nil {
		logrus.Errorf("failed to get release, err: %v", err)
		return nil, err
	}

	if _, err := d.executeDeploy(order, releaseResp); err != nil {
		logrus.Errorf("failed to execute deploy, order id: %s, err: %v", req.DeploymentOrderId, err)
		return nil, err
	}

	return order, nil
}

func (d *DeploymentOrder) executeDeploy(order *dbclient.DeploymentOrder, releaseResp *apistructs.ReleaseGetResponseData) (map[uint64]*apistructs.DeploymentCreateResponseDTO, error) {
	// compose runtime create requests
	rtCreateReqs, err := d.composeRuntimeCreateRequests(order, releaseResp)
	if err != nil {
		return nil, fmt.Errorf("failed to compose runtime create requests, err: %v", err)
	}

	deployResponse := make(map[uint64]*apistructs.DeploymentCreateResponseDTO)
	applicationsStatus := make(apistructs.DeploymentOrderStatusMap)

	// create runtimes
	for _, rtCreateReq := range rtCreateReqs {
		runtimeCreateResp, err := d.rt.Create(order.Operator, rtCreateReq)
		if err != nil {
			return nil, fmt.Errorf("failed to create runtime %s, cluster: %s, release id: %s, err: %v",
				rtCreateReq.Name, rtCreateReq.ClusterName, rtCreateReq.ReleaseID, err)
		}
		deployResponse[runtimeCreateResp.ApplicationID] = runtimeCreateResp
		applicationsStatus[rtCreateReq.Extra.ApplicationName] = apistructs.DeploymentOrderStatusItem{
			DeploymentID:     runtimeCreateResp.DeploymentID,
			AppID:            runtimeCreateResp.ApplicationID,
			DeploymentStatus: apistructs.DeploymentStatusInit,
			RuntimeID:        runtimeCreateResp.RuntimeID,
		}
	}

	// marshal applications status
	jsonAppStatus, err := json.Marshal(applicationsStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal applications status, err: %v", err)
	}

	// update deployment order status
	order.StartedAt = time.Now()
	order.Status = string(jsonAppStatus)
	if err := d.db.UpdateDeploymentOrder(order); err != nil {
		return nil, err
	}

	return deployResponse, nil
}

func (d *DeploymentOrder) composeDeploymentOrder(release *apistructs.ReleaseGetResponseData,
	req *apistructs.DeploymentOrderCreateRequest) (*dbclient.DeploymentOrder, error) {
	var (
		t         = req.Type
		orderId   = req.Id
		workspace = req.Workspace
	)

	order := &dbclient.DeploymentOrder{
		Type:        t,
		ID:          orderId,
		Workspace:   workspace,
		Operator:    user.ID(req.Operator),
		ProjectId:   uint64(release.ProjectID),
		ReleaseId:   release.ReleaseID,
		ProjectName: release.ProjectName,
	}

	params, err := d.fetchApplicationsParams(release, workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch deployment params, err: %v", err)
	}

	paramsJson, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params, err: %v", err)
	}

	order.Params = string(paramsJson)

	switch t {
	case apistructs.TypePipeline:
		order.ApplicationId, order.ApplicationName = release.ApplicationID, release.ApplicationName
		return order, nil
	case apistructs.TypeApplicationRelease:
		order.ApplicationId, order.ApplicationName = release.ApplicationID, release.ApplicationName
	}

	return order, nil
}

func (d *DeploymentOrder) fetchApplicationsParams(r *apistructs.ReleaseGetResponseData, workspace string) (map[string]*apistructs.DeploymentOrderParam, error) {
	ret := make(map[string]*apistructs.DeploymentOrderParam, 0)

	if r.IsProjectRelease {
		for _, ar := range r.ApplicationReleaseList {
			params, err := d.fetchDeploymentParams(ar.ApplicationID, workspace)
			if err != nil {
				return nil, err
			}
			ret[ar.ApplicationName] = params
		}
	} else {
		params, err := d.fetchDeploymentParams(r.ApplicationID, workspace)
		if err != nil {
			return nil, err
		}
		ret[r.ApplicationName] = params
	}

	return ret, nil
}

func (d *DeploymentOrder) fetchDeploymentParams(applicationId int64, workspace string) (*apistructs.DeploymentOrderParam, error) {
	configNsTmpl := "app-%d-%s"

	deployConfig, fileConfig, err := d.bdl.FetchDeploymentConfigDetail(fmt.Sprintf(configNsTmpl, applicationId, strings.ToUpper(workspace)))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch deployment config, err: %v", err)
	}

	params := make(apistructs.DeploymentOrderParam, 0)

	for _, c := range deployConfig {
		params = append(params, &apistructs.DeploymentOrderParamData{
			Key:     c.Key,
			Value:   c.Value,
			Type:    "ENV",
			Encrypt: c.Encrypt,
			Comment: c.Comment,
		})
	}

	for _, c := range fileConfig {
		params = append(params, &apistructs.DeploymentOrderParamData{
			Key:     c.Key,
			Value:   c.Value,
			Type:    "FILE",
			Encrypt: c.Encrypt,
			Comment: c.Comment,
		})
	}

	return &params, nil
}

func (d *DeploymentOrder) composeRuntimeCreateRequests(order *dbclient.DeploymentOrder, r *apistructs.ReleaseGetResponseData) ([]*apistructs.RuntimeCreateRequest, error) {
	if order == nil || r == nil {
		return nil, fmt.Errorf("deployment order or release response data is nil")
	}

	var (
		ret       = make([]*apistructs.RuntimeCreateRequest, 0)
		projectId = uint64(r.ProjectID)
		orgId     = uint64(r.OrgID)
		workspace = order.Workspace
	)

	projectInfo, err := d.bdl.GetProject(projectId)
	if err != nil {
		return nil, fmt.Errorf("failed to get project info, id: %d, err: %v", projectId, err)
	}

	// get cluster name with workspace
	clusterName, ok := projectInfo.ClusterConfig[workspace]
	if !ok {
		return nil, fmt.Errorf("cluster not found at workspace: %s", workspace)
	}

	// parse operator
	operator := order.Operator.String()

	// deployment order id
	deploymentOrderId := order.ID

	// parse params
	var orderParams map[string]*apistructs.DeploymentOrderParam
	if err := json.Unmarshal([]byte(order.Params), &orderParams); err != nil {
		return nil, fmt.Errorf("failed to unmarshal params, err: %v", err)
	}

	t := order.Type
	source := apistructs.RuntimeSource(release)
	if t == apistructs.TypePipeline {
		source = apistructs.TypePipeline
	}

	if r.IsProjectRelease {
		for _, ar := range r.ApplicationReleaseList {
			rtCreateReq := &apistructs.RuntimeCreateRequest{
				Name:              ar.ApplicationName,
				DeploymentOrderId: deploymentOrderId,
				ReleaseVersion:    r.Version,
				ReleaseID:         ar.ReleaseID,
				Source:            source,
				Operator:          operator,
				ClusterName:       clusterName,
				Extra: apistructs.RuntimeCreateRequestExtra{
					OrgID:           orgId,
					ProjectID:       projectId,
					ApplicationName: ar.ApplicationName,
					ApplicationID:   uint64(ar.ApplicationID),
					DeployType:      release,
					Workspace:       workspace,
					BuildID:         0, // Deprecated
				},
				SkipPushByOrch: false,
			}
			ret = append(ret, rtCreateReq)

			paramJson, err := json.Marshal(orderParams[ar.ApplicationName])
			if err != nil {
				return nil, err
			}

			rtCreateReq.Param = string(paramJson)
		}
	} else {
		rtCreateReq := &apistructs.RuntimeCreateRequest{
			Name:              order.ApplicationName,
			DeploymentOrderId: deploymentOrderId,
			ReleaseVersion:    r.Version,
			ReleaseID:         r.ReleaseID,
			Source:            source,
			Operator:          operator,
			ClusterName:       clusterName,
			Extra: apistructs.RuntimeCreateRequestExtra{
				OrgID:           orgId,
				ProjectID:       projectId,
				ApplicationID:   uint64(r.ApplicationID),
				ApplicationName: r.ApplicationName,
				DeployType:      release,
				Workspace:       workspace,
				BuildID:         0, // Deprecated
			},
			SkipPushByOrch: false,
		}

		paramJson, err := json.Marshal(orderParams[r.ApplicationName])
		if err != nil {
			return nil, err
		}

		rtCreateReq.Param = string(paramJson)

		if t == apistructs.TypePipeline {
			branch, ok := r.Labels[gitBranchLabel]
			if !ok {
				return nil, fmt.Errorf("failed to get release branch in release %s", r.ReleaseID)
			}
			rtCreateReq.Name = branch
			rtCreateReq.Extra.DeployType = ""
		}

		ret = append(ret, rtCreateReq)
	}

	return ret, nil
}

func (d *DeploymentOrder) parseAppsInfoWithOrder(order *dbclient.DeploymentOrder) (map[int64]string, error) {
	ret := make(map[int64]string)
	switch order.Type {
	case apistructs.TypeProjectRelease:
		releaseResp, err := d.bdl.GetRelease(order.ReleaseId)
		if err != nil {
			return nil, err
		}
		for _, r := range releaseResp.ApplicationReleaseList {
			ret[r.ApplicationID] = r.ApplicationName
		}
	default:
		ret[order.ApplicationId] = order.ApplicationName
	}
	return ret, nil
}

func (d *DeploymentOrder) parseAppsInfoWithRelease(releaseResp *apistructs.ReleaseGetResponseData) map[int64]string {
	ret := make(map[int64]string)
	if releaseResp.IsProjectRelease {
		for _, r := range releaseResp.ApplicationReleaseList {
			ret[r.ApplicationID] = r.ApplicationName
		}
	} else {
		ret[releaseResp.ApplicationID] = releaseResp.ApplicationName
	}

	return ret
}

func parseOrderType(t string, isProjectRelease bool) string {
	var orderType string
	if t == apistructs.TypePipeline {
		orderType = apistructs.TypePipeline
	} else if isProjectRelease {
		orderType = apistructs.TypeProjectRelease
	} else {
		orderType = apistructs.TypeApplicationRelease
	}

	return orderType
}

func covertParamsType(param *apistructs.DeploymentOrderParam) *apistructs.DeploymentOrderParam {
	if param == nil {
		return param
	}
	for _, data := range *param {
		data.Type = convertConfigType(data.Type)
	}
	return param
}
