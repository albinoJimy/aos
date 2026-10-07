package main

// GOLDENS DA BASE do AOS-505 — GERADOS, NÃO ESCRITOS À MÃO.
//
// Medidos na base deste ticket (o commit ce189122, antes de qualquer alteração de código do
// AOS-505), com o run de referência do AOS-490 (`run-490-nativo`, projecção nativa 1.0.0) e o
// provider de ensaio a emitir os cabeçalhos que o proxy de produção emite. É contra eles que
// [TestAOS505_No_Off_SaoOsBytesDaBase] prova que, com a governação da rota desligada, o nó grava
// os mesmos bytes que gravava antes.

// aos505BaseTiposDeEvento são os tipos dos eventos do run, pela ordem.
var aos505BaseTiposDeEvento = []string{
	"run.state.transition",
	"run.toolset.frozen",
	"step.checkpoint",
	"step.checkpoint",
	"turn.recorded",
	"step.checkpoint",
	"tool.call.mediated",
	"step.ledger.applied",
	"step.checkpoint",
	"replay.captured",
	"step.checkpoint",
	"step.checkpoint",
	"step.checkpoint",
	"turn.recorded",
	"step.checkpoint",
	"replay.captured",
	"step.checkpoint",
	"run.state.transition",
}

// aos505BaseTurnos são os payloads dos `turn.recorded` do run, byte a byte.
var aos505BaseTurnos = []string{
	`{"turn":1,"manifest":{"schema_version":"1.0","prompt_hash":"sha256:b3322bd759e47d54cd08d12cc284611981ff389700c5ef91e209af0fb4489747","system_hash":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","assembly_version":"1.4.0","model":{"model_id":"gpt-4o","served_model_id":"gpt-4o","seed":0},"tools":[{"name":"arquivo","version":"1.0.0","digest":"sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4"},{"name":"beta","version":"1.0.0","digest":"sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4"},{"name":"counter","version":"1.0.0","digest":"sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4"}],"projection":"native","projection_version":"1.0.0","completion":{"mode":"observe"}},"input_tokens":11,"output_tokens":7,"cost_micro_usd":0,"custo_nao_derivado":true,"cache_read_tokens":8,"tool_calls_requested":1,"final":false,"stop_reason":"tool_calls","tools_offered":3}`,
	`{"turn":2,"manifest":{"schema_version":"1.0","prompt_hash":"sha256:46dabc9057d403243983af34bda627de8f10eee866ca112b72270d7804e822d0","system_hash":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","assembly_version":"1.4.0","model":{"model_id":"gpt-4o","served_model_id":"gpt-4o","seed":0},"tools":[{"name":"arquivo","version":"1.0.0","digest":"sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4"},{"name":"beta","version":"1.0.0","digest":"sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4"},{"name":"counter","version":"1.0.0","digest":"sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4"}],"projection":"native","projection_version":"1.0.0","completion":{"mode":"observe"}},"input_tokens":11,"output_tokens":7,"cost_micro_usd":0,"custo_nao_derivado":true,"cache_read_tokens":8,"tool_calls_requested":0,"final":true,"stop_reason":"stop","tools_offered":3}`,
}

// aos505BaseCapturas é a parte EM CLARO (`response`) de cada `replay.captured` do run, byte a
// byte. O resto do payload é o envelope selado por-titular e o relógio, que não são
// deterministas.
var aos505BaseCapturas = []string{
	`{"final":false,"input_tokens":11,"output_tokens":7,"cost_micro_usd":0,"custo_nao_derivado":true,"cache_read_tokens":8,"stop_reason":"tool_calls"}`,
	`{"final":false,"input_tokens":11,"output_tokens":7,"cost_micro_usd":0,"custo_nao_derivado":true,"cache_read_tokens":8,"stop_reason":"stop"}`,
}

// aos505BaseChavesDaCaptura são as chaves de topo do payload de um `replay.captured`.
var aos505BaseChavesDaCaptura = []string{
	"observed_at_unix_nano",
	"response",
	"schema_version",
	"sealed_content",
	"sealed_subject",
	"turn",
}

// aos505BaseFamiliasDoMetrics são as famílias do `/metrics` do nó depois do run (`# TYPE`),
// por ordem alfabética.
var aos505BaseFamiliasDoMetrics = []string{
	"aos_backup_scheduler_armed gauge",
	"aos_build_info gauge",
	"aos_draining gauge",
	"aos_eventstore_healthy gauge",
	"aos_gc_cycles_total counter",
	"aos_goroutines gauge",
	"aos_ingress_credential_denials_total counter",
	"aos_mediation_denials_total counter",
	"aos_mediation_escalations_total counter",
	"aos_mediation_permits_total counter",
	"aos_mediation_record_failures_total counter",
	"aos_memory_alloc_bytes gauge",
	"aos_memory_sys_bytes gauge",
	"aos_model_turns_total counter",
	"aos_plan_queue_ceiling gauge",
	"aos_plan_queue_ceiling_per_submitter gauge",
	"aos_plan_queue_claimable gauge",
	"aos_plan_queue_pending gauge",
	"aos_plan_queue_refused_per_submitter_total counter",
	"aos_plan_queue_refused_total counter",
	"aos_ready gauge",
	"aos_retention_scheduler_armed gauge",
	"aos_runs_completed_without_tool_call_total counter",
	"aos_runs_finished_by_last_tool_outcome_total counter",
	"aos_runs_finished_total counter",
	"aos_runs_hosted_total counter",
	"aos_runs_output_source_total counter",
	"aos_runs_suspended gauge",
	"aos_slo_evaluations_total counter",
	"aos_slo_evaluator_armed gauge",
	"aos_slo_evaluator_panics_total counter",
	"aos_tool_calls_repeated_total counter",
	"aos_tool_calls_total counter",
	"aos_trajectory_streams_active gauge",
	"aos_up gauge",
	"aos_worm_partitions gauge",
	"aos_worm_seal_failures_total counter",
}
