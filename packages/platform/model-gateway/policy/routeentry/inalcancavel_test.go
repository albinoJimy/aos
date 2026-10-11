package routeentry_test

// AOS-519 — O QUE UM PERFIL ASSINADO NÃO ALCANÇA.
//
// A allowlist, a soberania, o veredicto do kernel e a mediação do Reference Monitor não são
// campos de um perfil nem de um registo de entrada. Aqui tenta-se cada uma das quatro, com um
// registo VALIDAMENTE ASSINADO pelo dono — o pior caso: quem assina quer mesmo relaxar a garantia
// — e o nó recusa a pasta. Relaxar uma garantia não se faz por esta via.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

func TestAOS519_UmPerfilAssinadoNaoAlcancaAsGarantias(t *testing.T) {
	// noPerfil põe um campo a mais dentro do perfil; noRegisto, ao lado dele.
	noPerfil := func(campo string) func(map[string]any) {
		return func(d map[string]any) {
			d["perfil"] = json.RawMessage(strings.Replace(rt.ProfileJSON, `{"requested"`, `{`+campo+`,"requested"`, 1))
		}
	}
	noRegisto := func(nome string, valor any) func(map[string]any) {
		return func(d map[string]any) { d[nome] = valor }
	}
	casos := []caso{
		// 1. A ALLOWLIST: um perfil não autoriza um nome, nem acrescenta modelos ou regiões.
		{nome: "allowlist: campo no perfil", causa: routeentry.CauseBadProfile, registo: noPerfil(`"allowlist":{"models":["*"]}`)},
		{nome: "allowlist: campo no registo", causa: routeentry.CauseEntryUnreadable, registo: noRegisto("allowlist", map[string]any{"models": []string{"*"}})},
		{nome: "allowlist: alias que ela nao autoriza", causa: routeentry.CauseNameNotAllowlisted, perfil: strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"modelo-nao-autorizado"`, 1)},
		// 2. A SOBERANIA: um perfil não muda a região nem o board, e um registo de outro ambiente não entra.
		{nome: "soberania: regioes no perfil", causa: routeentry.CauseBadProfile, registo: noPerfil(`"regions":["us-east"]`)},
		{nome: "soberania: regioes no registo", causa: routeentry.CauseEntryUnreadable, registo: noRegisto("regioes_permitidas", []string{"us-east"})},
		{nome: "soberania: registo para outra regiao", causa: routeentry.CauseOtherEnvironment, registo: noRegisto("regiao", "us-east")},
		// 3. O VEREDICTO DO KERNEL: um perfil não muda o modo de conclusão nem o contrato de um run.
		{nome: "veredicto do kernel: modo de conclusao no perfil", causa: routeentry.CauseBadProfile, registo: noPerfil(`"completion_mode":"off"`)},
		{nome: "veredicto do kernel: no registo", causa: routeentry.CauseEntryUnreadable, registo: noRegisto("completion_verdict", "off")},
		// 4. A MEDIAÇÃO DO REFERENCE MONITOR: um perfil não declara tools, nem as dispensa da mediação.
		{nome: "mediacao: tools no perfil", causa: routeentry.CauseBadProfile, registo: noPerfil(`"tools":[{"name":"shell","mediated":false}]`)},
		{nome: "mediacao: capacidade desconhecida", causa: routeentry.CauseBadProfile, registo: func(d map[string]any) {
			d["perfil"] = json.RawMessage(strings.Replace(rt.ProfileJSON, `["tools"]`, `["tools","unmediated_tools"]`, 1))
		}},
		{nome: "mediacao: no registo", causa: routeentry.CauseEntryUnreadable, registo: noRegisto("sem_mediacao", true)},
	}
	for _, c := range casos {
		t.Run(c.nome, c.correr)
	}
}
