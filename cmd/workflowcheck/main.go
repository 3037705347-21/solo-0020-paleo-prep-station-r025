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
		specimen, err := registerSpecimen(client, server.URL)
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
		specimen, err := registerSpecimen(client, server.URL)
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

	case "log-preparation-batch":
		specimenA, planA, err := approvedPlanSetup(client, server.URL, "PPS-2026-101")
		if err != nil {
			return err
		}
		specimenB, planB, err := approvedPlanSetup(client, server.URL, "PPS-2026-102")
		if err != nil {
			return err
		}
		var accepted struct {
			Items []struct {
				PlanID   string `json:"plan_id"`
				Sequence int    `json:"sequence"`
				Percent  int    `json:"percent"`
			} `json:"items"`
		}
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/progress/batch",
			contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
				{PlanID: planA.ID, Stage: "基质剥离", Percent: 40, Note: "班组批量录入", RecordedBy: "preparator-chen"},
				{PlanID: planB.ID, Stage: "表面加固", Percent: 100, Note: "班组批量录入", RecordedBy: "preparator-chen"},
			}},
			http.StatusCreated,
			&accepted,
		); err != nil {
			return err
		}
		if len(accepted.Items) != 2 || accepted.Items[0].Sequence != 1 || accepted.Items[1].Sequence != 1 {
			return errors.New("batch did not return one first-sequence entry per plan")
		}
		var snapshotA planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+planA.ID, nil, http.StatusOK, &snapshotA); err != nil {
			return err
		}
		if snapshotA.Plan.Status != "active" || len(snapshotA.Progress) != 1 || snapshotA.Progress[0].Percent != 40 {
			return errors.New("first plan did not reach active with a single 40% entry")
		}
		var snapshotB planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+planB.ID, nil, http.StatusOK, &snapshotB); err != nil {
			return err
		}
		if snapshotB.Plan.Status != "completed" || len(snapshotB.Progress) != 1 || snapshotB.Progress[0].Percent != 100 {
			return errors.New("second plan did not complete with a single 100% entry")
		}
		var fetchedA specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimenA.ID, nil, http.StatusOK, &fetchedA); err != nil {
			return err
		}
		if fetchedA.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedA.Status)
		}
		var fetchedB specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimenB.ID, nil, http.StatusOK, &fetchedB); err != nil {
			return err
		}
		if fetchedB.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedB.Status)
		}
		// 批内重复方案必须整体失败。
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/progress/batch",
			contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
				{PlanID: planA.ID, Stage: "重复一", Percent: 60, Note: "重复", RecordedBy: "preparator-chen"},
				{PlanID: planA.ID, Stage: "重复二", Percent: 70, Note: "重复", RecordedBy: "preparator-chen"},
			}},
			http.StatusConflict,
			nil,
		); err != nil {
			return err
		}
		// 后段未知方案失败时，前段合法条目不得写入。
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/progress/batch",
			contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
				{PlanID: planA.ID, Stage: "薄片修整", Percent: 60, Note: "前段", RecordedBy: "preparator-chen"},
				{PlanID: "plan-missing", Stage: "不存在", Percent: 50, Note: "后段", RecordedBy: "preparator-chen"},
			}},
			http.StatusNotFound,
			nil,
		); err != nil {
			return err
		}
		// 比例回退必须整体失败。
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/progress/batch",
			contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
				{PlanID: planA.ID, Stage: "比例回退", Percent: 30, Note: "回退", RecordedBy: "preparator-chen"},
			}},
			http.StatusConflict,
			nil,
		); err != nil {
			return err
		}
		// 已完成方案的终态冲突必须整体失败。
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/progress/batch",
			contracts.ProgressBatchRequest{Items: []contracts.ProgressBatchItem{
				{PlanID: planB.ID, Stage: "终态写入", Percent: 100, Note: "终态", RecordedBy: "preparator-chen"},
			}},
			http.StatusConflict,
			nil,
		); err != nil {
			return err
		}
		// 上述失败都不得留下部分进度。
		var after planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+planA.ID, nil, http.StatusOK, &after); err != nil {
			return err
		}
		if after.Plan.Status != "active" || len(after.Progress) != 1 || after.Progress[0].Percent != 40 {
			return errors.New("failed batches left partial progress behind")
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
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	return registerSpecimenWithCatalog(client, baseURL, "PPS-2026-001")
}

func registerSpecimenWithCatalog(client *http.Client, baseURL, catalogNumber string) (specimenView, error) {
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
	specimen, err := registerSpecimenWithCatalog(client, baseURL, catalogNumber)
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
