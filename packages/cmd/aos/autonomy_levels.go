package main

// NÍVEIS DE AUTONOMIA (AOS-087) — o que torna o `escalate` ALCANÇÁVEL.
//
// O veredicto `escalate` do Reference Monitor — a entrada do bridge de aprovação humana
// (AOS-021) — NÃO vem de uma regra Cedar: Cedar só exprime permit/deny. Vem do ORÁCULO DE
// AUTONOMIA, que o PDP consulta depois de uma decisão de base `permit`: compõe o nível
// corrente do par (agente, domínio) com a classe de risco da acção e, se o modo de
// oversight exigir um humano (suggest/confirm/batch), REBAIXA o permit para escalate.
//
// Sem oráculo ligado, `applyAutonomy` é NO-OP e o escalate NUNCA dispara — o bridge de
// aprovação fica construído mas inalcançável. Este ficheiro liga-o por configuração.
//
// PORQUE OPT-IN E NÃO LIGADO POR OMISSÃO: o registo é FAIL-CLOSED — um par sem nível
// registado resolve L0, cujo oversight é `suggest` para TODAS as classes. Ligar o oráculo
// com um registo vazio faria CADA tool call exigir aprovação humana individual, o que
// pararia qualquer deployment existente. Quais agentes correm em que domínios e a que
// nível é uma decisão POR-DEPLOYMENT, não um default que se possa inventar. Sem a
// variável, o comportamento é exactamente o de antes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// ErrBadAutonomyLevels — AOS_AUTONOMY_LEVELS está definido mas é inválido. Fail-closed:
// quem declara níveis obtém-nos bem-formados ou o nó recusa arrancar. Um par mal escrito
// que fosse ignorado em silêncio deixaria o agente a correr sob L0 (tudo escalado) ou —
// pior — sob um nível diferente do pretendido.
var ErrBadAutonomyLevels = errors.New("aos: AOS_AUTONOMY_LEVELS mal configurado — formato `agente:dominio=Ln` separado por virgulas (ex.: `agt-1:http=L4,agt-1:fs=L5`), com Ln em L0..L5. L0=sugestao (tudo escala), L1=confirma cada accao, L2=lote (danger confirma), L3=tiering SA-ROC, L4=corre e so danger confirma, L5=corre e danger fica em amostragem post-hoc")

// autonomyLevelSpec é uma entrada declarada: o par (agente, domínio) e o seu nível.
type autonomyLevelSpec struct {
	agent  string
	domain string
	level  autonomy.Level
}

// parseAutonomyLevels lê AOS_AUTONOMY_LEVELS. Vazio ⇒ (nil, nil): oráculo NÃO ligado,
// comportamento inalterado.
func parseAutonomyLevels() ([]autonomyLevelSpec, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_AUTONOMY_LEVELS"))
	if raw == "" {
		return nil, nil
	}
	var out []autonomyLevelSpec
	for _, entrada := range strings.Split(raw, ",") {
		entrada = strings.TrimSpace(entrada)
		if entrada == "" {
			continue
		}
		par, nivel, ok := strings.Cut(entrada, "=")
		if !ok {
			return nil, fmt.Errorf("%w: entrada %q sem `=`", ErrBadAutonomyLevels, entrada)
		}
		agente, dominio, ok := strings.Cut(strings.TrimSpace(par), ":")
		if !ok {
			return nil, fmt.Errorf("%w: entrada %q sem `agente:dominio`", ErrBadAutonomyLevels, entrada)
		}
		agente, dominio = strings.TrimSpace(agente), strings.TrimSpace(dominio)
		if agente == "" || dominio == "" {
			return nil, fmt.Errorf("%w: entrada %q com agente ou dominio vazio", ErrBadAutonomyLevels, entrada)
		}
		lvl, err := parseAutonomyLevel(strings.TrimSpace(nivel))
		if err != nil {
			return nil, fmt.Errorf("%w: entrada %q: %v", ErrBadAutonomyLevels, entrada, err)
		}
		out = append(out, autonomyLevelSpec{agent: agente, domain: dominio, level: lvl})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: lista vazia", ErrBadAutonomyLevels)
	}
	return out, nil
}

// parseAutonomyLevel traduz "L0".."L5" (case-insensitive) no [autonomy.Level].
func parseAutonomyLevel(s string) (autonomy.Level, error) {
	switch strings.ToUpper(s) {
	case "L0":
		return autonomy.L0, nil
	case "L1":
		return autonomy.L1, nil
	case "L2":
		return autonomy.L2, nil
	case "L3":
		return autonomy.L3, nil
	case "L4":
		return autonomy.L4, nil
	case "L5":
		return autonomy.L5, nil
	default:
		return autonomy.L0, fmt.Errorf("nivel %q desconhecido (use L0..L5)", s)
	}
}

// buildAutonomyOracle constrói o registo de níveis a partir das entradas declaradas.
// nil ⇒ oráculo não ligado (o PDP não aplica oversight de autonomia e nada escala).
//
// O actor registado é a CONFIGURAÇÃO do nó: a alteração de nível é um facto auditável e
// o registo recusa promoções sem motivo nem atribuição. Aqui a atribuição é honesta —
// quem decidiu foi quem escreveu a variável de ambiente do deployment.
func buildAutonomyOracle(ctx context.Context, specs []autonomyLevelSpec) (autonomy.Oracle, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	reg := autonomy.NewLevelRegistry()
	for _, s := range specs {
		if _, err := reg.SetLevel(ctx, s.agent, s.domain, s.level,
			"provisionamento por AOS_AUTONOMY_LEVELS", "config:node"); err != nil {
			return nil, fmt.Errorf("%w: registar %s:%s=%s: %v", ErrBadAutonomyLevels, s.agent, s.domain, s.level, err)
		}
	}
	return reg, nil
}
