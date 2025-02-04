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

package spec

import (
	"time"
)

type CIV3BuildCache struct {
	ID          int64     `json:"id" xorm:"pk autoincr"`
	Name        string    `json:"name"`
	ClusterName string    `json:"clusterName"`
	LastPullAt  time.Time `json:"lastPullAt"`
	CreatedAt   time.Time `json:"createdAt" xorm:"created"`
	UpdatedAt   time.Time `json:"updatedAt" xorm:"updated"`
	DeletedAt   time.Time `xorm:"deleted"`
}

func (*CIV3BuildCache) TableName() string {
	return "ci_v3_build_caches"
}
