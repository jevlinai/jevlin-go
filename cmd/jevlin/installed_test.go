package main

import "testing"

func TestInstalledPlanRequiresSuccessfulIntegration(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    agentPlan
		fail int
		want bool
	}{
		{"empty", agentPlan{}, 0, false},
		{"committed", agentPlan{writes: []agentWrite{{}}}, 0, true},
		{"partial", agentPlan{writes: []agentWrite{{}}}, 1, false},
		{"safety only", agentPlan{writes: []agentWrite{{safety: true}}}, 0, false},
		{"refused", agentPlan{writes: []agentWrite{{}}, refused: []string{"host"}}, 0, false},
		{"aborted", agentPlan{writes: []agentWrite{{}}, aborted: "stopped"}, 0, false},
		{"unanswered", agentPlan{writes: []agentWrite{{}}, unanswerable: true}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedPlan(&tc.p, tc.fail); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
