package bancoensaio

// AOS-522 — AS BARREIRAS ENTRE A CORRIDA E O PACOTE, UMA A UMA.
//
// Entre «a corrida acabou» e «há um pacote por assinar» há quatro barreiras independentes
// (cabeçalho de qualificar.go). Cada teste abaixo ataca UMA, deixando as outras satisfeitas: usa
// as costuras de teste do `qualificar` para trocar o que está em memória ou no disco entre dois
// passos. Tirar a barreira avermelha o seu teste — é o que as mutações dirigidas conferem.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// aos522Reselar reescreve o relatório de um ficheiro com uma mudança e com o digest do NOVO
// conteúdo: fica um relatório que confere o seu próprio digest. Devolve esse digest. É o que faz
// quem troca um relatório com cuidado — o digest dá integridade, não autenticidade.
func aos522Reselar(t *testing.T, caminho string, mudar func(doc map[string]any)) string {
	t.Helper()
	cru, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(cru, &doc); err != nil {
		t.Fatal(err)
	}
	mudar(doc)
	novo, digest := routeentrytest.SealReport(t, doc)
	if err := os.WriteFile(caminho, novo, 0o600); err != nil {
		t.Fatal(err)
	}
	return digest
}

// aos522SemPacote confere que a corrida não deixou pacote: nem a pasta, nem um ficheiro com a forma
// de um registo. O relatório e o resumo ficam.
func aos522SemPacote(t *testing.T, saida string) string {
	t.Helper()
	for _, n := range aos522Ficheiros(t, saida) {
		if strings.HasPrefix(n, PastaDoPacote) || strings.Contains(n, "registo") {
			t.Errorf("ficou %s na pasta de saida", n)
		}
	}
	resumo, err := os.ReadFile(filepath.Join(saida, NomeDoResumoDeUmaPagina))
	if err != nil || !strings.Contains(string(resumo), TituloDeNadaParaAssinar) || strings.Contains(string(resumo), TituloDoQueSeAssina) {
		t.Errorf("o resumo tinha de dizer que nao ha nada para assinar (%v):\n%s", err, resumo)
	}
	return aos522NumaLinha(string(resumo))
}

// BARREIRA 1 — o código de saída E o relatório. A corrida sai com 0 e o relatório em memória não
// diz `qualificado`, ou não vale para produção, ou não é do modo real, ou não está completa: nada
// é escrito, e o comando NÃO sai com 0. (O relatório no disco continua bom e o seu digest confere:
// só esta barreira o pára.)
func TestAOS522_Barreira1_OCodigoDeSaidaSozinhoNaoChega(t *testing.T) {
	for nome, estragar := range map[string]func(r *Relatorio){
		"veredicto recusado":       func(r *Relatorio) { r.QualificacaoDoModelo.Veredicto = VeredictoRecusado },
		"veredicto inconclusivo":   func(r *Relatorio) { r.QualificacaoDoModelo.Veredicto = VeredictoInconclusivo },
		"nao vale para producao":   func(r *Relatorio) { r.QualificacaoDoModelo.ValeParaProducao = false },
		"modo que nao e o real":    func(r *Relatorio) { r.Modo = ModoProxy },
		"corrida que nao completa": func(r *Relatorio) { r.Terminou = TerminouInterrompid },
	} {
		p := aos522NovoPosto(t, ComportamentoCumpre)
		p.amb.entreACorridaEAPreparacao = func(feita *corridaFeita) { estragar(feita.relatorio) }
		saida := filepath.Join(t.TempDir(), "saida")
		e := executar(t, p.amb, p.real(saida, aos522Perfil)...)
		if e.codigo != SaidaErro || !strings.Contains(e.stderr, "nao dizem o mesmo") || strings.Contains(e.stdout, "route-profile-sign") {
			t.Errorf("%s: codigo %d (quer %d, e nunca 0)\n%s", nome, e.codigo, SaidaErro, e.stderr)
		}
		aos522SemPacote(t, saida)
	}
	// A função, pelos dois lados: um relatório bom com um código que não é 0 também não chega.
	p := aos522NovoPosto(t, ComportamentoCumpre)
	var bom *Relatorio
	p.amb.entreACorridaEAPreparacao = func(feita *corridaFeita) { bom = feita.relatorio }
	if e := executar(t, p.amb, p.real(filepath.Join(t.TempDir(), "saida"), aos522Perfil)...); e.codigo != SaidaOK || bom == nil {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	if !veredictoPositivo(SaidaOK, bom) {
		t.Error("um qualificado do modo real com codigo 0 e positivo")
	}
	for _, codigo := range []int{SaidaErro, SaidaRecusada, SaidaParouAMeio, SaidaModeloRecusado, SaidaInconclusivo, SaidaQualificadoEmEnsaio, SaidaSoPlano} {
		if veredictoPositivo(codigo, bom) {
			t.Errorf("com o codigo %d nao ha veredicto positivo, diga o relatorio o que disser", codigo)
		}
	}
}

// BARREIRA 2 — o relatório no disco é o que a corrida escreveu. Entre a corrida e a preparação
// alguém TROCA o ficheiro por outro relatório, bem feito: `qualificado`, do modo real, do mesmo
// perfil, com o digest do seu próprio conteúdo. As conferências do nó aceitavam-no; o que o pára é
// o digest não ser o da corrida. Causa própria, e nenhum pacote.
func TestAOS522_Barreira2_ORelatorioTrocadoNoDiscoNaoSeEmpacota(t *testing.T) {
	p := aos522NovoPosto(t, ComportamentoCumpre)
	var trocado string
	p.amb.entreACorridaEAPreparacao = func(feita *corridaFeita) {
		trocado = aos522Reselar(t, feita.emJSON, func(doc map[string]any) { doc["data_utc"] = "2020-01-01T00:00:00Z" })
		// O controlo do teste: o relatório trocado passa nas conferências do nó.
		cru, _ := os.ReadFile(feita.emJSON)
		if v, err := routeentry.ReadVerdict(cru); err != nil || v.Verdict != routeentry.VerdictQualified || !v.ValidForProduction || v.Digest == feita.relatorio.DigestDoRelatorio {
			t.Errorf("o relatorio trocado tinha de ser um qualificado valido com OUTRO digest: %+v %v", v, err)
		}
	}
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos522Perfil)...)
	if e.codigo != SaidaErro || !strings.Contains(e.stderr, "relatorio_trocado_depois_da_corrida") || trocado == "" {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	if resumo := aos522SemPacote(t, saida); !strings.Contains(resumo, "relatorio_trocado_depois_da_corrida") {
		t.Errorf("o resumo nao diz a causa:\n%s", resumo)
	}
	// Um relatório que nem o seu digest confere (um byte mudado à mão) também não se empacota.
	p = aos522NovoPosto(t, ComportamentoCumpre)
	p.amb.entreACorridaEAPreparacao = func(feita *corridaFeita) {
		cru, _ := os.ReadFile(feita.emJSON)
		_ = os.WriteFile(feita.emJSON, []byte(strings.Replace(string(cru), `"terminou": "completa"`, `"terminou": "outra"`, 1)), 0o600)
	}
	saida = filepath.Join(t.TempDir(), "saida")
	if e := executar(t, p.amb, p.real(saida, aos522Perfil)...); e.codigo != SaidaPacoteRecusado || !strings.Contains(e.stderr, "causa="+routeentry.CauseReportInvalid) {
		t.Errorf("relatorio com o digest partido: codigo %d\n%s", e.codigo, e.stderr)
	}
	aos522SemPacote(t, saida)
}

// BARREIRA 3 — as conferências do nó, antes de haver ficheiro. O relatório no disco é trocado por
// um que diz `recusado` (ou que é de um ensaio, ou de outro perfil), com o digest certo, E o
// relatório em memória é posto de acordo com esse digest: as barreiras 1 e 2 passam. Só o
// CheckReport do nó o pára — com a causa do nó, e o código 11.
func TestAOS522_Barreira3_SoOCheckReportDoNoParaUmRelatorioQueNaoQualifica(t *testing.T) {
	qualificacao := func(doc map[string]any) map[string]any { return doc["qualificacao_do_modelo"].(map[string]any) }
	for causa, mudar := range map[string]func(doc map[string]any){
		routeentry.CauseNotQualified:            func(doc map[string]any) { qualificacao(doc)["veredicto"] = VeredictoRecusado },
		routeentry.CauseVerdictNotForProduction: func(doc map[string]any) { qualificacao(doc)["vale_para_producao"] = false },
		routeentry.CauseVerdictOfOtherProfile: func(doc map[string]any) {
			qualificacao(doc)["rota_qualificada"].(map[string]any)["digest_do_perfil"] = "sha256:" + strings.Repeat("c", 64)
		},
	} {
		p := aos522NovoPosto(t, ComportamentoCumpre)
		p.amb.entreACorridaEAPreparacao = func(feita *corridaFeita) {
			// O registo cita o perfil do relatório em memória (o do candidato); o do disco pode ser outro.
			feita.relatorio.DigestDoRelatorio = aos522Reselar(t, feita.emJSON, mudar)
		}
		saida := filepath.Join(t.TempDir(), "saida")
		e := executar(t, p.amb, p.real(saida, aos522Perfil)...)
		if e.codigo != SaidaPacoteRecusado || !strings.Contains(e.stderr, "o no recusaria o pacote no arranque: causa="+causa) {
			t.Errorf("%s: codigo %d (quer %d)\n%s", causa, e.codigo, SaidaPacoteRecusado, e.stderr)
		}
		if resumo := aos522SemPacote(t, saida); !strings.Contains(resumo, "causa="+causa) {
			t.Errorf("%s: o resumo nao diz a causa do no:\n%s", causa, resumo)
		}
	}
}

// BARREIRA 4 — o que ficou escrito é o que se conferiu. Entre a escrita do pacote e a releitura,
// alguém mexe num dos dois ficheiros: num caso para um registo que o nó até aceitava (outra fatia),
// noutro para um relatório que diz `recusado`. O pacote é retirado, com causa própria.
func TestAOS522_Barreira4_OPacoteAlteradoDepoisDeEscritoERetirado(t *testing.T) {
	for nome, mexer := range map[string]func(pacote string){
		"o registo, para outra fatia valida": func(pacote string) {
			caminho := filepath.Join(pacote, "gpt-4o"+routeentry.SuffixEntry)
			cru, _ := os.ReadFile(caminho)
			novo := strings.Replace(string(cru), `"fatia_inicial_percentagem": 10`, `"fatia_inicial_percentagem": 100`, 1)
			if novo == string(cru) {
				t.Fatal("o teste nao mexeu no registo")
			}
			// O controlo: o registo mexido continua a passar na leitura do nó — é a comparação byte a
			// byte que o apanha, e não a conferência.
			if _, err := routeentry.ParseEntry([]byte(novo)); err != nil {
				t.Fatalf("o registo mexido tinha de continuar legivel pelo no: %v", err)
			}
			_ = os.WriteFile(caminho, []byte(novo), 0o600)
		},
		"o relatorio, para recusado": func(pacote string) {
			aos522Reselar(t, filepath.Join(pacote, "gpt-4o"+routeentry.SuffixReport), func(doc map[string]any) {
				doc["qualificacao_do_modelo"].(map[string]any)["veredicto"] = VeredictoRecusado
			})
		},
		"um ficheiro que desaparece": func(pacote string) { _ = os.Remove(filepath.Join(pacote, "gpt-4o"+routeentry.SuffixReport)) },
	} {
		p := aos522NovoPosto(t, ComportamentoCumpre)
		p.amb.depoisDeEscreverOPacote = mexer
		saida := filepath.Join(t.TempDir(), "saida")
		e := executar(t, p.amb, p.real(saida, aos522Perfil)...)
		if e.codigo != SaidaErro || !strings.Contains(e.stderr, "pacote_alterado_depois_de_escrito") || strings.Contains(e.stdout, "route-profile-sign") {
			t.Errorf("%s: codigo %d\n%s", nome, e.codigo, e.stderr)
		}
		aos522SemPacote(t, saida)
	}
}

// A COMPOSIÇÃO DOS PERFIS NO `conferir`. Com os perfis de hoje, o que o gateway recusa ao
// compor-se já foi recusado pelo LoadDir; a conferência existe para o dia em que não for. Faz-se
// falhar a composição e confere-se que o `conferir` recusa — e que sem ela aceitava.
func TestAOS522_Conferir_AComposicaoDosPerfisTambemRecusa(t *testing.T) {
	p := aos522NovoPosto(t, ComportamentoCumpre)
	saida := filepath.Join(t.TempDir(), "saida")
	if e := executar(t, p.amb, p.real(saida, aos522Perfil)...); e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	pacote := filepath.Join(saida, PastaDoPacote)
	chave := routeentrytest.NewKey(t)
	registo, _ := os.ReadFile(filepath.Join(pacote, "gpt-4o"+routeentry.SuffixEntry))
	if err := os.WriteFile(filepath.Join(pacote, "gpt-4o"+routeentry.SuffixSignature), []byte(routeentry.Sign(registo, chave.Priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"conferir", "--pacote", pacote, "--ancora", chave.Anchor(), "--board", routeentrytest.Board, "--regiao", routeentrytest.Region}
	if e := executar(t, Ambiente{}, args...); e.codigo != SaidaOK {
		t.Fatalf("o pacote bom tinha de passar: codigo %d\n%s", e.codigo, e.stderr)
	}
	deSempre := comporPerfis
	t.Cleanup(func() { comporPerfis = deSempre })
	vistos := 0
	comporPerfis = func(perfis ...modelgateway.RouteProfile) error {
		vistos = len(perfis)
		return errors.New("a composicao recusa")
	}
	if e := executar(t, Ambiente{}, args...); e.codigo != SaidaPacoteRecusado || vistos != 1 || strings.Contains(e.stdout, "ACEITE") || !strings.Contains(e.stderr, "nao se compoem") {
		t.Errorf("com a composicao a recusar: codigo %d, perfis vistos %d\n%s%s", e.codigo, vistos, e.stdout, e.stderr)
	}
	// E a composição de sempre é a do gateway: dois candidatos com o mesmo nome não se compõem.
	comporPerfis = deSempre
	perfil, _ := modelgateway.ParseRouteProfile([]byte(aos522Perfil))
	if comporPerfis(perfil) != nil || comporPerfis(perfil, perfil) == nil {
		t.Error("a composicao de sempre tinha de aceitar um candidato e recusar dois com o mesmo nome")
	}
}

// aos522AllowlistExterna escreve uma allowlist EXTERNA assinada com uma chave do teste — a via que
// um nó usa quando não quer os nomes fixos da embebida — e devolve a pasta e a âncora.
func aos522AllowlistExterna(t *testing.T, board, alias, regiao string) (dir, ancora string) {
	t.Helper()
	politica := []byte(`{"version":"gw-allowlist-de-teste/v1","default":"deny","rules":[{"id":"teste","board":"` + board + `","models":["` + alias + `"],"regions":["` + regiao + `"]}]}`)
	digest, err := allowlist.Digest(politica)
	if err != nil {
		t.Fatal(err)
	}
	chave := routeentrytest.NewKey(t)
	dir = t.TempDir()
	for nome, conteudo := range map[string][]byte{
		allowlist.BundlePolicyFile: politica,
		allowlist.BundleSigFile:    []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(chave.Priv, []byte(digest))) + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, nome), conteudo, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, chave.Anchor()
}

// O RESUMO MOSTRA O QUE FICOU ESCRITO, com valores que não são os de sempre: outro alias (que a
// tabela em código não tem), outro board, outra região, outra fatia, e um perfil com parâmetros e
// versão da projecção. O alias é autorizado por uma allowlist EXTERNA — no plano do `qualificar` e
// no `conferir`. Cada valor do bloco «o que vais assinar» é o do registo lido de volta do pacote.
func TestAOS522_ValoresNaoTriviais_OResumoEODoRegistoEscrito(t *testing.T) {
	const alias, board, regiao, fatia = "modelo-novo-do-teste", "board-de-ensaio", "regiao-de-ensaio", "37"
	perfil := `{"requested":"` + alias + `","expected_model":"openai/` + modeloKimiDeTeste + `","wire_class":"openai-chat-completions","capabilities":["tools"],"projection_version":"1.3.0","params":{"max_tokens":16000}}`
	dir, ancoraDaAllowlist := aos522AllowlistExterna(t, board, alias, regiao)
	p := aos522NovoPosto(t, ComportamentoCumpre)
	argumentos := func(saida string, extra ...string) []string {
		a := p.real(saida, perfil, extra...)
		a[9], a[11], a[13] = fatia, board, regiao
		return a
	}

	// SEM a allowlist externa o plano confere a embebida, que não tem este alias: 11, com a causa do
	// nó, antes de enviar — e não depois de gasta uma qualificação e de assinado o registo.
	sem := filepath.Join(t.TempDir(), "saida")
	if e := executar(t, p.amb, argumentos(sem)...); e.codigo != SaidaPacoteRecusado || p.lanc.lancamentos != 0 || !strings.Contains(e.stderr, "causa="+routeentry.CauseNameNotAllowlisted) || !strings.Contains(e.stderr, "EMBEBIDA") {
		t.Fatalf("sem a allowlist externa: codigo %d, lancamentos %d\n%s", e.codigo, p.lanc.lancamentos, e.stderr)
	}
	if _, err := os.Stat(sem); err == nil {
		t.Error("a recusa pela allowlist escreveu na pasta de saida")
	}
	// Com a âncora errada a allowlist não se carrega: nada é conferido nem enviado.
	outra := routeentrytest.NewKey(t)
	if e := executar(t, p.amb, argumentos(filepath.Join(t.TempDir(), "saida"), "--allowlist-dir", dir, "--ancora-da-allowlist", outra.Anchor())...); e.codigo != SaidaUso || p.lanc.lancamentos != 0 {
		t.Errorf("ancora da allowlist errada: codigo %d\n%s", e.codigo, e.stderr)
	}
	// Com a allowlist externa, mas para OUTRA região: a allowlist não autoriza.
	fora := argumentos(filepath.Join(t.TempDir(), "saida"), "--allowlist-dir", dir, "--ancora-da-allowlist", ancoraDaAllowlist)
	fora[13] = "outra-regiao"
	if e := executar(t, p.amb, fora...); e.codigo != SaidaPacoteRecusado || p.lanc.lancamentos != 0 || !strings.Contains(e.stderr, "causa="+routeentry.CauseNameNotAllowlisted) || !strings.Contains(e.stderr, "EXTERNA") {
		t.Errorf("regiao que a allowlist externa nao autoriza: codigo %d\n%s", e.codigo, e.stderr)
	}

	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, argumentos(saida, "--allowlist-dir", dir, "--ancora-da-allowlist", ancoraDaAllowlist)...)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	pacote := filepath.Join(saida, PastaDoPacote)
	registo, err := os.ReadFile(filepath.Join(pacote, alias+routeentry.SuffixEntry))
	if err != nil {
		t.Fatal(err)
	}
	// O que o NÓ lê do registo escrito — e é isso, valor a valor, que o resumo tem de mostrar.
	doNo, err := routeentry.ParseEntry(registo)
	if err != nil || doNo.InitialSharePercent != 37 || doNo.Board != board || doNo.Region != regiao || doNo.Profile.Requested != alias {
		t.Fatalf("registo escrito = %+v (%v)", doNo, err)
	}
	cru, _ := os.ReadFile(filepath.Join(saida, NomeDoResumoDeUmaPagina))
	resumo := aos522NumaLinha(string(cru))
	bloco := resumo[strings.Index(resumo, TituloDoQueSeAssina):]
	for _, quer := range []string{
		`alias pedido: "` + alias + `"; modelo esperado: "openai/` + modeloKimiDeTeste + `"; fatia inicial: 37%`,
		`board: "` + board + `"; regiao: "` + regiao + `"`,
		`wire: "openai-chat-completions"; capacidades: [tools]; projection_version: 1.3.0; devolver: nunca`,
		"params do pedido: max_tokens=16000",
		"ACRESCENTA-SE a tabela de perfis, que nao tem este alias — segundo a tabela em codigo DESTE binario (revisao ",
		"digest do perfil: " + doNo.ProfileDigest, "digest do relatorio: " + doNo.ReportDigest,
		"digest imagem proxy: " + doNo.ProxyImageDigest, "digest do registo: " + doNo.EntryDigest,
	} {
		if !strings.Contains(bloco, quer) {
			t.Errorf("o bloco do que se assina nao diz %q:\n%s", quer, cru)
		}
	}
	for _, naoQuer := range []string{"fatia inicial: 10%", `"board-eu"`, `"gpt-4o"`, "SUBSTITUI", digestPorConhecer} {
		if strings.Contains(bloco, naoQuer) {
			t.Errorf("o bloco do que se assina diz %q, que nao e deste registo", naoQuer)
		}
	}
	// A linha de conferir leva a allowlist externa com que o plano foi feito.
	if !strings.Contains(e.stdout, `--board "`+board+`" --regiao "`+regiao+`" --allowlist-dir "`+dir+`" --ancora-da-allowlist "`+ancoraDaAllowlist+`"`) {
		t.Errorf("a linha de conferir nao leva a allowlist externa:\n%s", e.stdout)
	}

	// O `conferir` COM A ALLOWLIST EXTERNA. Assina-se por fora, com uma chave do teste.
	chave := routeentrytest.NewKey(t)
	if err := os.WriteFile(filepath.Join(pacote, alias+routeentry.SuffixSignature), []byte(routeentry.Sign(registo, chave.Priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conferir := []string{"conferir", "--pacote", pacote, "--ancora", chave.Anchor(), "--board", board, "--regiao", regiao}
	com := append(append([]string(nil), conferir...), "--allowlist-dir", dir, "--ancora-da-allowlist", ancoraDaAllowlist)
	if c := executar(t, Ambiente{}, com...); c.codigo != SaidaOK || !strings.Contains(c.stdout, "ACEITE") || !strings.Contains(c.stdout, "EXTERNA") || !strings.Contains(c.stdout, "fatia inicial 37%") || !strings.Contains(c.stdout, doNo.EntryDigest) {
		t.Errorf("conferir com a allowlist externa: codigo %d\n%s%s", c.codigo, c.stdout, c.stderr)
	}
	// Sem ela confere-se a embebida, que não autoriza o alias: a causa do nó.
	if c := executar(t, Ambiente{}, conferir...); c.codigo != SaidaPacoteRecusado || !strings.Contains(c.stderr, "causa="+routeentry.CauseNameNotAllowlisted+" registo="+alias) {
		t.Errorf("conferir sem a allowlist externa: codigo %d\n%s", c.codigo, c.stderr)
	}
	// As duas opções vão juntas; e com a âncora errada a allowlist não se carrega.
	for nome, extra := range map[string][]string{
		"so a pasta":     {"--allowlist-dir", dir},
		"so a ancora":    {"--ancora-da-allowlist", ancoraDaAllowlist},
		"ancora errada":  {"--allowlist-dir", dir, "--ancora-da-allowlist", outra.Anchor()},
		"pasta sem nada": {"--allowlist-dir", t.TempDir(), "--ancora-da-allowlist", ancoraDaAllowlist},
	} {
		if c := executar(t, Ambiente{}, append(append([]string(nil), conferir...), extra...)...); c.codigo != SaidaUso || strings.Contains(c.stdout, "ACEITE") {
			t.Errorf("conferir, %s: codigo %d\n%s", nome, c.codigo, c.stderr)
		}
	}
	// A âncora dos perfis não pode ser a da allowlist externa: as duas não são intermutáveis.
	if c := executar(t, Ambiente{}, "conferir", "--pacote", pacote, "--ancora", ancoraDaAllowlist, "--board", board, "--regiao", regiao, "--allowlist-dir", dir, "--ancora-da-allowlist", ancoraDaAllowlist); c.codigo != SaidaPacoteRecusado || !strings.Contains(c.stderr, "causa="+routeentry.CauseSharedTrustAnchor) {
		t.Errorf("ancora dos perfis igual a da allowlist externa: codigo %d\n%s", c.codigo, c.stderr)
	}
}
