package main

// AOS-520 (PR 2) — COM UM CANDIDATO A SERVIR, AS FAMÍLIAS QUE JÁ EXISTIAM NÃO MUDAM DE BYTES.
//
// O PR 2 acrescenta três famílias ao `/metrics`, e só com um candidato a servir. A inércia sem
// candidato a servir continua presa em `aos520_inercia_test.go` (PR 1), sem alteração. Este ficheiro
// prende a outra metade: com o candidato a servir, as linhas das famílias que já existiam e a lista
// dos nomes das famílias (tiradas as três novas) são as do commit do PR 1.
//
// COMO SE MEDIU. Este ficheiro só usa símbolos que existem no commit da base (be8b169d, o PR 1): foi
// corrido lá com `AOS520_MEDIR_A_BASE=1`, num checkout destacado, e os dois digests que imprimiu são
// as constantes de baixo. Na base as três famílias novas não existem, e o filtro não tira nada.

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// aos520sFamiliasNovas são as três famílias do PR 2.
var aos520sFamiliasNovas = []string{
	"aos_model_turns_by_model_total",
	"aos_model_response_rejected_by_model_total",
	"aos_runs_finished_by_model_total",
}

// aos520sSeparar parte o `/metrics` em: as linhas das famílias novas; as linhas das famílias de
// contagem determinista que já existiam (as do modelo, as dos runs e as das tool calls); e os nomes
// de todas as famílias que não são novas.
func aos520sSeparar(metricas string) (novas, existentes, familias []string) {
	nomes := map[string]bool{}
	for _, linha := range strings.Split(metricas, "\n") {
		nova := false
		for _, f := range aos520sFamiliasNovas {
			nova = nova || strings.Contains(linha, f)
		}
		if nova {
			novas = append(novas, linha)
			continue
		}
		if strings.HasPrefix(linha, "# TYPE ") {
			if campos := strings.Fields(linha); len(campos) >= 3 {
				nomes[campos[2]] = true
			}
		}
		for _, prefixo := range []string{"aos_model_", "aos_runs_finished", "aos_runs_completed_without_tool_call_total", "aos_runs_hosted_total", "aos_tool_calls"} {
			if strings.Contains(linha, prefixo) {
				existentes = append(existentes, linha)
				break
			}
		}
	}
	for f := range nomes {
		familias = append(familias, f)
	}
	sort.Strings(familias)
	return novas, existentes, familias
}

// Medidos no commit da base (be8b169d) com este ficheiro: um nó com `on` e a fatia a 10%, o run "0"
// (do candidato) e o run "a" (do titular).
const (
	aos520sExistentesDaBase = "e48462de69e32cea7193829081e3c4a427e25a719726c8405ee7aab47af41cfc"
	aos520sFamiliasDaBase   = "21895440af96f753c9f338d1cb94603b70bb06dd62cd5ac250ead7ec3972929e"
)

func TestAOS520_Series_ComCandidatoAServir_AsFamiliasQueExistiamSaoAsDaBase(t *testing.T) {
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(10))
	n.correr(t, "0", nil) // balde 168: do candidato
	n.correr(t, "a", nil) // balde 8230: do titular
	novas, existentes, familias := aos520sSeparar(aos520cMetricas(t, n.aos486No))
	if len(existentes) < 20 || len(familias) < 20 {
		t.Fatalf("o /metrics nao foi lido como se esperava: %d linhas, %d familias", len(existentes), len(familias))
	}
	gotExistentes, gotFamilias := aos520Sha(existentes...), aos520Sha(familias...)
	if os.Getenv("AOS520_MEDIR_A_BASE") != "" {
		t.Logf("BASE: existentes=%s familias=%s (linhas novas: %d)", gotExistentes, gotFamilias, len(novas))
		return
	}
	if gotExistentes != aos520sExistentesDaBase {
		t.Fatalf("com o candidato a servir, as linhas das familias que ja existiam mudaram: %s; na base %s\n%s", gotExistentes, aos520sExistentesDaBase, strings.Join(existentes, "\n"))
	}
	if gotFamilias != aos520sFamiliasDaBase {
		t.Fatalf("a lista das familias (sem as tres novas) mudou: %s; na base %s\n%q", gotFamilias, aos520sFamiliasDaBase, familias)
	}
	if len(novas) == 0 {
		t.Fatal("com o candidato a servir as tres familias novas tinham de existir")
	}
}
