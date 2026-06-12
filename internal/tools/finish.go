package tools

// FinishSchema is the OpenAI tool definition for finish — the structured end
// of a run. Unlike the other tools it is not executed here: the agent loop
// intercepts the call, emits a {"type":"finish"} event carrying outcome and
// summary, and stops. The outcome lets the runner distinguish "done" from
// "blocked" instead of inferring success from a commit-less exit, and the
// summary becomes the run's closing report without relying on the model
// remembering to post a comment.
func FinishSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        FinishName,
			"description": "End the run with a structured outcome. Call this exactly once, as your last action: after committing completed work, after answering a question via comment, or as soon as you determine you cannot proceed. Do not keep working after calling finish.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome": map[string]any{
						"type": "string",
						"enum": []string{"completed", "blocked", "needs_info"},
						"description": "completed: the task is done (code committed, or a no-change answer given). " +
							"blocked: the environment or task state prevents progress (broken checkout, missing access, contradictory requirements) - the run will be marked failed with your summary. " +
							"needs_info: you posted a question on the work item and a human must answer before work can continue.",
					},
					"summary": map[string]any{
						"type":        "string",
						"description": "Brief report of what was done, answered, or what is blocking. A few sentences.",
					},
				},
				"required": []string{"outcome", "summary"},
			},
		},
	}
}
