package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
}

type planView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Status     string `json:"status"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

type progressEntryView struct {
	ID       string `json:"id"`
	PlanID   string `json:"plan_id"`
	Sequence int    `json:"sequence"`
	Percent  int    `json:"percent"`
}

type progressBatchView struct {
	Items []progressEntryView `json:"items"`
}

type errorView struct {
	Code string `json:"code"`
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	handler := httpapi.NewRouter(station.New(repository.NewMemory()))
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL, "PPS-2026-001")
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL, "PPS-2026-001")
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil
	case "log-preparation-batch":
		return checkProgressBatch(client, server.URL)
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL, catalogNumber string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: catalogNumber,
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	return approvedPlanSetup(client, baseURL, "PPS-2026-001")
}

func approvedPlanSetup(client *http.Client, baseURL, catalogNumber string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL, catalogNumber)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func checkProgressBatch(client *http.Client, baseURL string) error {
	specimenA, planA, err := approvedPlanSetup(client, baseURL, "PPS-2026-101")
	if err != nil {
		return err
	}
	specimenB, planB, err := approvedPlanSetup(client, baseURL, "PPS-2026-102")
	if err != nil {
		return err
	}

	var first progressBatchView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
			{PlanID: planA.ID, Stage: "基质剥离", Percent: 40, Note: "批量首条", RecordedBy: "preparator-chen"},
			{PlanID: planB.ID, Stage: "表面加固", Percent: 100, Note: "批量完成", RecordedBy: "preparator-chen"},
		}},
		http.StatusCreated,
		&first,
	); err != nil {
		return err
	}
	if len(first.Items) != 2 {
		return fmt.Errorf("expected two batch entries, got %d", len(first.Items))
	}
	if first.Items[0].PlanID != planA.ID || first.Items[0].Sequence != 1 || first.Items[0].Percent != 40 {
		return errors.New("first batch entry does not match plan A")
	}
	if first.Items[1].PlanID != planB.ID || first.Items[1].Sequence != 1 || first.Items[1].Percent != 100 {
		return errors.New("second batch entry does not match plan B")
	}
	if err := expectPlanProgress(client, baseURL, planB.ID, "completed", []int{100}); err != nil {
		return err
	}
	var fetchedB specimenView
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+specimenB.ID, nil, http.StatusOK, &fetchedB); err != nil {
		return err
	}
	if fetchedB.Status != "stabilized" {
		return fmt.Errorf("expected stabilized specimen B, got %s", fetchedB.Status)
	}
	var fetchedA specimenView
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+specimenA.ID, nil, http.StatusOK, &fetchedA); err != nil {
		return err
	}
	if fetchedA.Status != "preparing" {
		return fmt.Errorf("expected preparing specimen A, got %s", fetchedA.Status)
	}

	var second progressBatchView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
			{PlanID: planA.ID, Stage: "围岩清理", Percent: 70, Note: "批量续录", RecordedBy: "preparator-chen"},
		}},
		http.StatusCreated,
		&second,
	); err != nil {
		return err
	}
	if len(second.Items) != 1 || second.Items[0].Sequence != 2 {
		return errors.New("follow-up batch did not continue the sequence for plan A")
	}

	var failure errorView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
			{PlanID: planA.ID, Stage: "断层扫描", Percent: 85, Note: "应整体回滚", RecordedBy: "preparator-chen"},
			{PlanID: "plan-missing", Stage: "基质剥离", Percent: 50, Note: "未知方案", RecordedBy: "preparator-chen"},
		}},
		http.StatusNotFound,
		&failure,
	); err != nil {
		return err
	}
	if failure.Code != "plan_not_found" {
		return fmt.Errorf("expected plan_not_found, got %s", failure.Code)
	}
	if err := expectPlanProgress(client, baseURL, planA.ID, "active", []int{40, 70}); err != nil {
		return err
	}

	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
			{PlanID: planA.ID, Stage: "酸洗", Percent: 80, Note: "重复方案", RecordedBy: "preparator-chen"},
			{PlanID: planA.ID, Stage: "加固", Percent: 90, Note: "重复方案", RecordedBy: "preparator-chen"},
		}},
		http.StatusConflict,
		&failure,
	); err != nil {
		return err
	}
	if failure.Code != "duplicate_plan_in_batch" {
		return fmt.Errorf("expected duplicate_plan_in_batch, got %s", failure.Code)
	}
	if err := expectPlanProgress(client, baseURL, planA.ID, "active", []int{40, 70}); err != nil {
		return err
	}

	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
			{PlanID: planA.ID, Stage: "酸洗", Percent: 60, Note: "比例回退", RecordedBy: "preparator-chen"},
		}},
		http.StatusConflict,
		&failure,
	); err != nil {
		return err
	}
	if failure.Code != "progress_regressed" {
		return fmt.Errorf("expected progress_regressed, got %s", failure.Code)
	}
	if err := expectPlanProgress(client, baseURL, planA.ID, "active", []int{40, 70}); err != nil {
		return err
	}

	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
			{PlanID: planA.ID, Stage: "加固", Percent: 90, Note: "应整体回滚", RecordedBy: "preparator-chen"},
			{PlanID: planB.ID, Stage: "补录", Percent: 50, Note: "已完成方案", RecordedBy: "preparator-chen"},
		}},
		http.StatusConflict,
		&failure,
	); err != nil {
		return err
	}
	if failure.Code != "plan_not_recordable" {
		return fmt.Errorf("expected plan_not_recordable, got %s", failure.Code)
	}
	if err := expectPlanProgress(client, baseURL, planA.ID, "active", []int{40, 70}); err != nil {
		return err
	}
	if err := expectPlanProgress(client, baseURL, planB.ID, "completed", []int{100}); err != nil {
		return err
	}

	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/progress/batch",
		contracts.ProgressBatchRequest{},
		http.StatusBadRequest,
		&failure,
	); err != nil {
		return err
	}
	if failure.Code != "empty_progress_batch" {
		return fmt.Errorf("expected empty_progress_batch, got %s", failure.Code)
	}
	return nil
}

func expectPlanProgress(client *http.Client, baseURL, planID, wantStatus string, wantPercents []int) error {
	var snapshot planSnapshot
	if err := requestJSON(client, http.MethodGet, baseURL+"/plans/"+planID, nil, http.StatusOK, &snapshot); err != nil {
		return err
	}
	if snapshot.Plan.Status != wantStatus {
		return fmt.Errorf("expected plan status %s, got %s", wantStatus, snapshot.Plan.Status)
	}
	if len(snapshot.Progress) != len(wantPercents) {
		return fmt.Errorf("expected %d progress entries, got %d", len(wantPercents), len(snapshot.Progress))
	}
	for index, want := range wantPercents {
		if snapshot.Progress[index].Percent != want {
			return fmt.Errorf("progress %d expected %d%%, got %d%%", index+1, want, snapshot.Progress[index].Percent)
		}
	}
	return nil
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}
