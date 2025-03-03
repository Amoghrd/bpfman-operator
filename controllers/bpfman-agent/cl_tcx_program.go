/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bpfmanagent

import (
	"context"
	"fmt"
	"reflect"

	bpfmaniov1alpha1 "github.com/bpfman/bpfman-operator/apis/v1alpha1"
	internal "github.com/bpfman/bpfman-operator/internal"
	gobpfman "github.com/bpfman/bpfman/clients/gobpfman/v1"
	"github.com/google/uuid"
)

// ClTcxProgramReconciler contains the info required to reconcile a TcxProgram
type ClTcxProgramReconciler struct {
	ReconcilerCommon
	ClProgramReconcilerCommon
	currentLink *bpfmaniov1alpha1.ClTcxAttachInfoState
}

func (r *ClTcxProgramReconciler) getProgId() *uint32 {
	return r.currentProgramState.ProgramId
}

func (r *ClTcxProgramReconciler) getProgType() internal.ProgramType {
	return internal.Tc
}

func (r *ClTcxProgramReconciler) getBpfmanProgType() gobpfman.BpfmanProgramType {
	return gobpfman.BpfmanProgramType_TCX
}

func (r *ClTcxProgramReconciler) getProgName() string {
	return r.currentProgram.Name
}

func (r *ClTcxProgramReconciler) shouldAttach() bool {
	return r.currentLink.ShouldAttach
}

func (r *ClTcxProgramReconciler) isAttached(ctx context.Context) bool {
	if r.currentProgramState.ProgramId == nil || r.currentLink.LinkId == nil {
		return false
	}
	return r.doesLinkExist(ctx, *r.currentProgramState.ProgramId, *r.currentLink.LinkId)
}

func (r *ClTcxProgramReconciler) getUUID() string {
	return r.currentLink.UUID
}

func (r *ClTcxProgramReconciler) getLinkId() *uint32 {
	return r.currentLink.LinkId
}

func (r *ClTcxProgramReconciler) setLinkId(id *uint32) {
	r.currentLink.LinkId = id
}

func (r *ClTcxProgramReconciler) setProgramLinkStatus(status bpfmaniov1alpha1.ProgramLinkStatus) {
	r.currentProgramState.ProgramLinkStatus = status
}

func (r *ClTcxProgramReconciler) getProgramLinkStatus() bpfmaniov1alpha1.ProgramLinkStatus {
	return r.currentProgramState.ProgramLinkStatus
}

func (r *ClTcxProgramReconciler) setCurrentLinkStatus(status bpfmaniov1alpha1.LinkStatus) {
	r.currentLink.LinkStatus = status
}

func (r *ClTcxProgramReconciler) getCurrentLinkStatus() bpfmaniov1alpha1.LinkStatus {
	return r.currentLink.LinkStatus
}

func (r *ClTcxProgramReconciler) getAttachRequest() *gobpfman.AttachRequest {

	attachInfo := &gobpfman.TCXAttachInfo{
		Priority:  r.currentLink.Priority,
		Iface:     r.currentLink.IfName,
		Direction: r.currentLink.Direction,
		Metadata:  map[string]string{internal.UuidMetadataKey: string(r.currentLink.UUID)},
	}

	if r.currentLink.ContainerPid != nil {
		netns := fmt.Sprintf("/host/proc/%d/ns/net", *r.currentLink.ContainerPid)
		attachInfo.Netns = &netns
	}

	return &gobpfman.AttachRequest{
		Id: *r.currentProgramState.ProgramId,
		Attach: &gobpfman.AttachInfo{
			Info: &gobpfman.AttachInfo_TcxAttachInfo{
				TcxAttachInfo: attachInfo,
			},
		},
	}
}

// updateLinks processes the *ProgramInfo and updates the list of links
// contained in *AttachInfoState.
func (r *ClTcxProgramReconciler) updateLinks(ctx context.Context, isBeingDeleted bool) error {
	r.Logger.Info("TCX updateAttachInfo()", "isBeingDeleted", isBeingDeleted)

	// Set ShouldAttach for all links in the node CRD to false.  We'll
	// update this in the next step for all links that are still
	// present.
	for i := range r.currentProgramState.TCXInfo.Links {
		r.currentProgramState.TCXInfo.Links[i].ShouldAttach = false
	}

	if isBeingDeleted {
		// If the program is being deleted, we don't need to do anything else.
		return nil
	}

	if r.currentProgram.TCXInfo != nil && r.currentProgram.TCXInfo.Links != nil {
		for _, attachInfo := range r.currentProgram.TCXInfo.Links {
			expectedLinks, error := r.getExpectedLinks(ctx, attachInfo)
			if error != nil {
				return fmt.Errorf("failed to get node links: %v", error)
			}
			for _, link := range expectedLinks {
				index := r.findLink(link)
				if index != nil {
					// Link already exists, so set ShouldAttach to true.
					r.currentProgramState.TCXInfo.Links[*index].AttachInfoStateCommon.ShouldAttach = true
				} else {
					// Link doesn't exist, so add it.
					r.Logger.Info("Link doesn't exist.  Adding it.")
					r.currentProgramState.TCXInfo.Links = append(r.currentProgramState.TCXInfo.Links, link)
				}
			}
		}
	}

	// If any existing link is no longer on a list of expected links
	// ShouldAttach will remain set to false and it will get detached in a
	// following step.
	// a following step.

	return nil
}

func (r *ClTcxProgramReconciler) findLink(attachInfoState bpfmaniov1alpha1.ClTcxAttachInfoState) *int {
	for i, a := range r.currentProgramState.TCXInfo.Links {
		// attachInfoState is the same as a if the the following fields are the
		// same: IfName, ContainerPid, Priority, and Direction.
		if a.IfName == attachInfoState.IfName && a.Priority == attachInfoState.Priority &&
			a.Direction == attachInfoState.Direction &&
			reflect.DeepEqual(a.ContainerPid, attachInfoState.ContainerPid) {
			return &i
		}
	}
	return nil
}

// processLinks calls reconcileBpfLink() for each link. It
// then updates the ProgramAttachStatus based on the updated status of each
// link.
func (r *ClTcxProgramReconciler) processLinks(ctx context.Context) error {
	r.Logger.Info("Processing attach info", "bpfFunctionName", r.currentProgram.Name)

	// The following map is used to keep track of links that need to be
	// removed.  If it's not empty at the end of the loop, we'll remove the
	// links.
	linksToRemove := make(map[int]bool)

	var lastReconcileLinkError error = nil
	for i := range r.currentProgramState.TCXInfo.Links {
		r.currentLink = &r.currentProgramState.TCXInfo.Links[i]
		remove, err := r.reconcileBpfLink(ctx, r)
		if err != nil {
			r.Logger.Error(err, "failed to reconcile bpf attachment", "index", i)
			// All errors are logged, but the last error is saved to return and
			// we continue to process the rest of the links so errors
			// don't block valid links.
			lastReconcileLinkError = err
		}

		if remove {
			r.Logger.Info("Marking link for removal", "index", i)
			linksToRemove[i] = true
		}
	}

	if len(linksToRemove) > 0 {
		r.Logger.Info("Removing links", "linksToRemove", linksToRemove)
		r.currentProgramState.TCXInfo.Links = r.removeLinks(r.currentProgramState.TCXInfo.Links, linksToRemove)
	}

	r.updateProgramAttachStatus()

	return lastReconcileLinkError
}

func (r *ClTcxProgramReconciler) updateProgramAttachStatus() {
	for _, link := range r.currentProgramState.TCXInfo.Links {
		if !isAttachSuccess(link.ShouldAttach, link.LinkStatus) {
			r.setProgramLinkStatus(bpfmaniov1alpha1.ProgAttachError)
			return
		}
	}
	r.setProgramLinkStatus(bpfmaniov1alpha1.ProgAttachSuccess)
}

// removeLinks removes links from a slice of links based on the keys in the map.
func (r *ClTcxProgramReconciler) removeLinks(links []bpfmaniov1alpha1.ClTcxAttachInfoState, linksToRemove map[int]bool) []bpfmaniov1alpha1.ClTcxAttachInfoState {
	var remainingLinks []bpfmaniov1alpha1.ClTcxAttachInfoState
	for i, a := range links {
		if _, ok := linksToRemove[i]; !ok {
			remainingLinks = append(remainingLinks, a)
		}
	}
	return remainingLinks
}

// getExpectedLinks expands *AttachInfo into a list of specific attach
// points.
func (r *ClTcxProgramReconciler) getExpectedLinks(ctx context.Context, attachInfo bpfmaniov1alpha1.ClTcxAttachInfo,
) ([]bpfmaniov1alpha1.ClTcxAttachInfoState, error) {
	interfaces, err := getInterfaces(&attachInfo.InterfaceSelector, r.ourNode)
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces for TcxProgram: %v", err)
	}

	nodeLinks := []bpfmaniov1alpha1.ClTcxAttachInfoState{}

	if attachInfo.Containers != nil {
		// There is a container selector, so see if there are any matching
		// containers on this node.
		containerInfo, err := r.Containers.GetContainers(
			ctx,
			attachInfo.Containers.Namespace,
			attachInfo.Containers.Pods,
			attachInfo.Containers.ContainerNames,
			r.Logger,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to get container pids: %v", err)
		}

		if containerInfo != nil && len(*containerInfo) != 0 {
			// Containers were found, so create links.
			for i := range *containerInfo {
				container := (*containerInfo)[i]
				for _, iface := range interfaces {
					containerPid := container.pid
					link := bpfmaniov1alpha1.ClTcxAttachInfoState{
						AttachInfoStateCommon: bpfmaniov1alpha1.AttachInfoStateCommon{
							ShouldAttach: true,
							UUID:         uuid.New().String(),
							LinkId:       nil,
							LinkStatus:   bpfmaniov1alpha1.ApAttachNotAttached,
						},
						IfName:       iface,
						ContainerPid: &containerPid,
						Priority:     attachInfo.Priority,
						Direction:    attachInfo.Direction,
					}
					nodeLinks = append(nodeLinks, link)
				}
			}
		}
	} else {
		for _, iface := range interfaces {
			link := bpfmaniov1alpha1.ClTcxAttachInfoState{
				AttachInfoStateCommon: bpfmaniov1alpha1.AttachInfoStateCommon{
					ShouldAttach: true,
					UUID:         uuid.New().String(),
					LinkId:       nil,
					LinkStatus:   bpfmaniov1alpha1.ApAttachNotAttached,
				},
				IfName:       iface,
				ContainerPid: nil,
				Priority:     attachInfo.Priority,
				Direction:    attachInfo.Direction,
			}
			nodeLinks = append(nodeLinks, link)
		}
	}

	return nodeLinks, nil
}

func (r *ClTcxProgramReconciler) getProgramLoadInfo() *gobpfman.LoadInfo {
	return &gobpfman.LoadInfo{
		Name:        r.currentProgram.Name,
		ProgramType: r.getBpfmanProgType(),
		Info:        nil,
	}
}
