package bancoensaio

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// AOS-528 — O PROXY EFÉMERO COM DUAS ROTAS: a do executor e a do conselheiro.

// aos528ConfigDeAntes é a configuração do proxy de uma rota, tal como o código de antes do
// AOS-528 a escrevia (a `openai/` com base). Uma corrida sem conselheiro tem de a escrever igual.
const aos528ConfigDeAntes = "model_list:\n  - model_name: \"rota-de-ensaio\"\n    litellm_params:\n      model: openai/modelo-x\n      api_key: os.environ/ENSAIO_API_KEY\n      api_base: os.environ/ENSAIO_API_BASE\n" +
	"litellm_settings:\n  drop_params: true\n  telemetry: false\n  num_retries: 0\n" +
	"router_settings:\n  num_retries: 0\n  disable_cooldowns: true\n" +
	"general_settings:\n  background_health_checks: false\n"

func TestAOS528_Proxy_SemConselheiroAConfigEADeAntes(t *testing.T) {
	cfg, err := configDoProxy("openai", "modelo-x", true)
	if err != nil || cfg != aos528ConfigDeAntes {
		t.Fatalf("a configuracao de uma rota mudou (err %v):\n%s", err, cfg)
	}
	semBase, _ := configDoProxy("anthropic", "modelo-y", false)
	if strings.Contains(semBase, "api_base") || strings.Contains(semBase, "CONSELHEIRO") || strings.Count(semBase, "model_name") != 1 {
		t.Errorf("a configuracao sem base mudou:\n%s", semBase)
	}
	// O arranque do contentor de uma rota não muda: nem argumentos nem ambiente do conselheiro.
	args, ambiente := arranqueDoContentorDoProxy("c", "r", "mestra-xyz", "chave-xyz", "https://base-xyz", time.Hour, time.Minute)
	if strings.Contains(strings.Join(args, " ")+strings.Join(ambiente, " "), "CONSELHEIRO") {
		t.Error("o arranque de uma rota so ganhou variaveis do conselheiro")
	}
}

func TestAOS528_Proxy_DuasRotasCadaUmaComASuaChaveEBase(t *testing.T) {
	// Executor pela OpenRouter, conselheiro pelo Kimi: dois fornecedores, duas chaves, duas bases.
	cfg, err := configDoProxyComRotas([]rotaDaConfig{
		{alias: AliasDaRota, prefixo: PrefixoDaOpenRouter, modelo: "anthropic/haiku-de-teste", comBase: true, envChave: envChaveDaRota, envBase: envBaseDaRota},
		{alias: AliasDoConselheiro, prefixo: "openai", modelo: "kimi-de-teste", comBase: true, envChave: envChaveDoConselheiro, envBase: envBaseDoConselheiro},
	})
	if err != nil {
		t.Fatal(err)
	}
	quer := "model_list:\n" +
		"  - model_name: \"rota-de-ensaio\"\n    litellm_params:\n      model: openrouter/anthropic/haiku-de-teste\n      api_key: os.environ/ENSAIO_API_KEY\n      api_base: os.environ/ENSAIO_API_BASE\n" +
		"  - model_name: \"rota-conselheiro\"\n    litellm_params:\n      model: openai/kimi-de-teste\n      api_key: os.environ/ENSAIO_CONSELHEIRO_API_KEY\n      api_base: os.environ/ENSAIO_CONSELHEIRO_API_BASE\n" +
		"litellm_settings:\n  drop_params: true\n  telemetry: false\n  num_retries: 0\n" +
		"router_settings:\n  num_retries: 0\n  disable_cooldowns: true\n" +
		"general_settings:\n  background_health_checks: false\n"
	if cfg != quer {
		t.Fatalf("configuracao de duas rotas:\n%s", cfg)
	}
	// Um conselheiro da Anthropic não tem base (o destino é o do adaptador), como o executor.
	cfg, _ = configDoProxyComRotas([]rotaDaConfig{
		{alias: AliasDaRota, prefixo: "openai", modelo: "k", comBase: true, envChave: envChaveDaRota, envBase: envBaseDaRota},
		{alias: AliasDoConselheiro, prefixo: "anthropic", modelo: "sonnet-de-teste", envChave: envChaveDoConselheiro, envBase: envBaseDoConselheiro},
	})
	if strings.Contains(cfg, envBaseDoConselheiro) || !strings.Contains(cfg, "model: anthropic/sonnet-de-teste\n      api_key: os.environ/ENSAIO_CONSELHEIRO_API_KEY\n") {
		t.Errorf("conselheiro da Anthropic:\n%s", cfg)
	}
	// Dois aliases iguais: o proxy serviria um com a chave do outro. Recusado.
	if _, err := configDoProxyComRotas([]rotaDaConfig{
		{alias: "x", prefixo: "openai", modelo: "a", envChave: envChaveDaRota},
		{alias: "x", prefixo: "openai", modelo: "b", envChave: envChaveDoConselheiro},
	}); !errors.Is(err, ErrProxy) {
		t.Errorf("aliases iguais: err = %v", err)
	}
	for _, mau := range []rotaDaConfig{
		{alias: AliasDoConselheiro, prefixo: "outro", modelo: "m"},
		{alias: AliasDoConselheiro, prefixo: "openai", modelo: "com espaco"},
		{alias: "com.ponto", prefixo: "openai", modelo: "m"},
	} {
		if _, err := configDoProxyComRotas([]rotaDaConfig{{alias: AliasDaRota, prefixo: "openai", modelo: "k"}, mau}); !errors.Is(err, ErrProxy) {
			t.Errorf("rota do conselheiro invalida %+v: err = %v", mau, err)
		}
	}
}

// A chave e a base do conselheiro vão como as da primeira rota: o valor só no AMBIENTE do processo
// docker, e nos argumentos só o nome, antes do `--entrypoint`.
func TestAOS528_Proxy_OsSegredosDoConselheiroSoNoAmbiente(t *testing.T) {
	const chave, base = "SENTINELA-CHAVE-DO-CONSELHEIRO-91ab", "https://api.kimi.com/SENTINELA-BASE-DO-CONSELHEIRO"
	c := RotaDoConselheiroNoProxy{Prefixo: "openai", Modelo: "k", Alias: AliasDoConselheiro}.ComSegredos(chave, base)
	args, ambiente := arranqueDoContentorDoProxy("c", "r", "mestra-xyz", "chave-xyz", "", time.Hour, time.Minute)
	args, ambiente = comAmbienteDoConselheiro(args, ambiente, c)
	linha := strings.Join(args, " ")
	if strings.Contains(linha, chave) || strings.Contains(linha, base) {
		t.Fatal("um segredo do conselheiro foi para os argumentos do processo")
	}
	if !strings.Contains(linha, "-e "+envChaveDoConselheiro+" -e "+envBaseDoConselheiro+" --entrypoint") {
		t.Errorf("os nomes das variaveis do conselheiro tinham de ir antes do --entrypoint: %s", linha)
	}
	amb := strings.Join(ambiente, "\n")
	if !strings.Contains(amb, envChaveDoConselheiro+"="+chave) || !strings.Contains(amb, envBaseDoConselheiro+"="+base) || !strings.Contains(amb, envChaveDaRota+"=chave-xyz") {
		t.Error("os valores tinham de ir no ambiente do processo docker")
	}
	// Sem base (Anthropic), só a chave.
	args, _ = arranqueDoContentorDoProxy("c", "r", "m", "k", "", time.Hour, time.Minute)
	args, _ = comAmbienteDoConselheiro(args, nil, RotaDoConselheiroNoProxy{Prefixo: "anthropic", Modelo: "s", Alias: AliasDoConselheiro}.ComSegredos(chave, ""))
	if strings.Contains(strings.Join(args, " "), envBaseDoConselheiro) {
		t.Error("sem base, nao vai a variavel da base")
	}
	// A rota não se deixa imprimir, e o lançador recusa o conselheiro com o provider falso em contentor.
	if s := c.String() + c.GoString(); strings.Contains(s, chave) || strings.Contains(s, base) {
		t.Error("a rota do conselheiro deixou-se imprimir com os segredos")
	}
	red := &Redactor{}
	_, err := (&LancadorDocker{Redactor: red}).Lancar(context.Background(), PedidoDeProxy{Prefixo: "openai", Modelo: "m", BinarioDoFalso: "x", Conselheiro: c})
	if err == nil || strings.Contains(err.Error(), chave) {
		t.Errorf("o lancador tinha de recusar o conselheiro com o falso em contentor: %v", err)
	}
	if got := red.Texto("a " + chave + " b " + base); strings.Contains(got, chave) || strings.Contains(got, base) {
		t.Error("o redactor do lancador tinha de passar a conhecer os segredos do conselheiro")
	}
}

// O QUE O LANÇADOR COMPÕE, com o conselheiro de OUTRO fornecedor: cada rota da configuração refere
// a variável da SUA chave e da SUA base, e o ambiente do processo docker dá a cada variável o valor
// do fornecedor certo. A chave do executor nunca chega à rota do conselheiro, nem a dele à do
// executor. É a composição que o [LancadorDocker] usa, sem Docker.
func TestAOS528_Proxy_OLancadorPoeCadaChaveNaSuaRota(t *testing.T) {
	const (
		chaveKimi, baseKimi     = "SENTINELA-CHAVE-DO-EXECUTOR-KIMI-77aa", "https://api.kimi.com/SENTINELA-BASE-DO-EXECUTOR"
		chaveAnthropic, mestra  = "SENTINELA-CHAVE-DO-CONSELHEIRO-ANTHROPIC-88bb", "mestra-do-teste"
		chaveOpenR, baseDaOpenR = "SENTINELA-CHAVE-DO-CONSELHEIRO-OPENROUTER-99cc", baseDaOpenRouter
	)
	rotaDe := func(cfg, alias string) string {
		_, depois, ok := strings.Cut(cfg, `model_name: "`+alias+`"`)
		if !ok {
			t.Fatalf("a configuracao nao tem a rota %s:\n%s", alias, cfg)
		}
		bloco, _, _ := strings.Cut(depois, "  - model_name:")
		bloco, _, _ = strings.Cut(bloco, "litellm_settings:")
		return bloco
	}
	valor := func(ambiente []string, nome string) string {
		for _, a := range ambiente {
			if v, ok := strings.CutPrefix(a, nome+"="); ok {
				return v
			}
		}
		return ""
	}
	for _, caso := range []struct {
		nome               string
		conselheiro        *RotaDoConselheiroNoProxy
		chave, base, model string
	}{
		{"conselheiro anthropic, sem base", RotaDoConselheiroNoProxy{Prefixo: "anthropic", Modelo: "sonnet-de-teste", Alias: AliasDoConselheiro}.ComSegredos(chaveAnthropic, ""), chaveAnthropic, "", "anthropic/sonnet-de-teste"},
		{"conselheiro openrouter, com base", RotaDoConselheiroNoProxy{Prefixo: PrefixoDaOpenRouter, Modelo: "google/gemini-de-teste", Alias: AliasDoConselheiro}.ComSegredos(chaveOpenR, baseDaOpenR), chaveOpenR, baseDaOpenR, "openrouter/google/gemini-de-teste"},
	} {
		p := PedidoDeProxy{Prefixo: "openai", Modelo: "kimi-de-teste", Conselheiro: caso.conselheiro}.ComSegredos(chaveKimi, baseKimi)
		cfg, args, ambiente, err := arranqueDoProxyComRotas(p, baseKimi, "c", "r", mestra, time.Hour, time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", caso.nome, err)
		}
		executor, conselheiro := rotaDe(cfg, AliasDaRota), rotaDe(cfg, AliasDoConselheiro)
		if !strings.Contains(executor, "model: openai/kimi-de-teste\n      api_key: os.environ/"+envChaveDaRota+"\n      api_base: os.environ/"+envBaseDaRota+"\n") ||
			strings.Contains(executor, "CONSELHEIRO") {
			t.Errorf("%s: rota do executor:\n%s", caso.nome, executor)
		}
		quer := "model: " + caso.model + "\n      api_key: os.environ/" + envChaveDoConselheiro + "\n"
		if caso.base != "" {
			quer += "      api_base: os.environ/" + envBaseDoConselheiro + "\n"
		}
		if !strings.Contains(conselheiro, quer) ||
			strings.Contains(conselheiro, envChaveDaRota+"\n") || strings.Contains(conselheiro, envBaseDaRota+"\n") || (caso.base == "" && strings.Contains(conselheiro, "api_base")) {
			t.Errorf("%s: rota do conselheiro:\n%s", caso.nome, conselheiro)
		}
		if valor(ambiente, envChaveDaRota) != chaveKimi || valor(ambiente, envBaseDaRota) != baseKimi ||
			valor(ambiente, envChaveDoConselheiro) != caso.chave || valor(ambiente, envBaseDoConselheiro) != caso.base {
			t.Errorf("%s: o ambiente deu a uma variavel o valor de outro fornecedor", caso.nome)
		}
		amb := strings.Join(ambiente, "\n")
		if strings.Count(amb, chaveKimi) != 1 || strings.Contains(amb, envChaveDoConselheiro+"="+chaveKimi) || strings.Contains(amb, envBaseDoConselheiro+"="+baseKimi) {
			t.Errorf("%s: a chave ou a base do executor chegou a rota do conselheiro", caso.nome)
		}
		linha := strings.Join(args, " ")
		if strings.Contains(linha, chaveKimi) || strings.Contains(linha, caso.chave) || !strings.Contains(linha, "-e "+envChaveDoConselheiro) {
			t.Errorf("%s: argumentos: %s", caso.nome, linha)
		}
	}
	// Sem conselheiro: a configuração e o arranque são os de sempre.
	cfg, args, ambiente, err := arranqueDoProxyComRotas(PedidoDeProxy{Prefixo: "openai", Modelo: "modelo-x"}.ComSegredos(chaveKimi, baseKimi), baseKimi, "c", "r", mestra, time.Hour, time.Minute)
	antesArgs, antesAmb := arranqueDoContentorDoProxy("c", "r", mestra, chaveKimi, baseKimi, time.Hour, time.Minute)
	if err != nil || cfg != aos528ConfigDeAntes || strings.Join(args, " ") != strings.Join(antesArgs, " ") || strings.Join(ambiente, "\n") != strings.Join(antesAmb, "\n") {
		t.Errorf("sem conselheiro o arranque mudou (err %v)", err)
	}
}
