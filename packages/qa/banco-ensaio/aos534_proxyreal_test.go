package bancoensaio

// AOS-534 — SPIKE DA SUB-FASE A6.1: A PARTE `video_url` ATRAVESSA O PROXY REAL?
//
// A A6.1 manda o vídeo inteiro ao modelo como uma parte `video_url` com uma data URL
// (`data:video/mp4;base64,…`), pela OpenRouter, através da imagem de produção do proxy (decisão
// do dono de 2026-10-18, opção A). Este cenário mede o primeiro elo: pedidos DIRECTOS ao proxy
// real (sem gateway nem runtime), pelas duas formas de rota que ele tem para a OpenRouter —
// `openrouter/<autor>/<modelo>` e `openai/<autor>/<modelo>` com a base da OpenRouter —, com texto
// e uma parte `video_url`, contra o falso do estado na forma da OpenRouter, que regista as partes
// que lhe chegam ([FalsoDeEstado.contarPartes]): o tipo, a classe da data URL e o sha256 dos
// bytes, nunca os bytes.
//
// Dois tamanhos: um MP4 pequeno e um de 4 MiB (cerca de 5,6 MB em base64), para ver se o proxy
// tem tecto de corpo. O MP4 é SINTÉTICO ([aos534MP4]): válido como contentor, não como vídeo.
//
// MEDE, NÃO JULGA: uma recusa ou uma alteração da parte pelo proxy é o resultado do spike, e vai
// no relatório (`AOS_BANCO_VIDEO_REPORT`), por rota e por tamanho. Fica vermelho só se o ensaio
// não se montar, se o proxy não chegar ao falso num pedido de controlo só com texto, ou se uma
// chave aparecer no relatório.
//
// PRECISA DE DOCKER, da imagem já descarregada e de um binário LINUX do banco. Só a pedido:
//
//	AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN=<binário linux> go test -run '^TestAOS534_ProxyReal_VideoPeloProxy$' -v -count=1 .
//
// O QUE NÃO PROVA: nada sobre a OpenRouter real — que modelos aceitam vídeo, com que limites de
// tamanho e duração, e a que preço. São os outros dois elos.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// aos534ModeloDeVideo é o nome, na OpenRouter, do modelo da rota do ensaio: um modelo que a
// OpenRouter documenta como capaz de vídeo. O falso responde a qualquer um; o nome só importa ao
// proxy, se ele decidir pela ficha do modelo o que fazer com a parte.
const aos534ModeloDeVideo = "google/gemini-2.5-flash"

// aos534BytesGrande é o tamanho da `mdat` do caso grande: 4 MiB de bytes, cerca de 5,6 MB em base64.
const aos534BytesGrande = 4 << 20

func TestAOS534_ProxyReal_VideoPeloProxy(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 ou AOS_BANCO_FALSO_BIN vazio — a parte video_url atras do proxy real precisa de Docker, " +
			"da imagem de producao do proxy e de um binario linux do banco. POR VERIFICAR nesta execucao: o que o proxy faz a parte video_url. " +
			"O registo das partes no falso, sem proxy, correu (TestAOS534_Falso_RegistaAParteDeVideoSemOsBytes).")
	}
	const chave = "sk-falso-aos534-video"
	videos := []struct {
		nome  string
		bytes []byte
	}{
		{"pequeno", aos534MP4(2000, 4096)},
		{"grande", aos534MP4(2000, aos534BytesGrande)},
	}
	medidas := map[string]any{"imagem": ImagemDoProxy, "modelo": aos534ModeloDeVideo}
	mestras := []string{}
	for _, prefixo := range []string{PrefixoDaOpenRouter, "openai"} {
		ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Minute)
		vivo, err := (&LancadorDocker{Redactor: &Redactor{}}).Lancar(ctx, PedidoDeProxy{
			Prefixo: prefixo, Modelo: aos534ModeloDeVideo, BinarioDoFalso: binario, Estado: EstadoExige, TurnosDoFalso: 1,
			FormaDoFalso: FormaOpenRouter, Vida: 12 * time.Minute,
		}.ComSegredos(chave, ""))
		if err != nil {
			cancelar()
			t.Fatalf("%s: o proxy nao arrancou: %v", prefixo, err)
		}
		mestras = append(mestras, vivo.ChaveMestra())
		doPrefixo := map[string]any{"rota": prefixo + "/" + aos534ModeloDeVideo}
		antes := &FormaNoFornecedor{}
		enviar := func(nome string, corpo []byte) map[string]any {
			medida := map[string]any{"bytes_do_pedido": len(corpo)}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, vivo.BaseURL+"/chat/completions", bytes.NewReader(corpo))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+vivo.ChaveMestra())
			inicio := time.Now()
			if resp, rerr := http.DefaultClient.Do(req); rerr != nil {
				medida["erro_de_transporte"] = true
			} else {
				lido, _ := io.ReadAll(io.LimitReader(resp.Body, maxCorpoDeErro))
				_ = resp.Body.Close()
				medida["http"] = resp.StatusCode
				if resp.StatusCode != http.StatusOK {
					medida["tipo_do_erro"] = classificarErro(resp.StatusCode, lido)
				}
			}
			medida["ms"] = time.Since(inicio).Milliseconds()
			// Numa recusa, a causa está no registo do proxy: só nomes de excepção e de função.
			if medida["http"] != http.StatusOK && vivo.Rasto != nil {
				if rasto := vivo.Rasto(); rasto != "" {
					medida["rasto_do_proxy"] = rasto
				}
			}
			depois, ferr := LerFormaDoFalso(ctx, vivo.EnderecoDoFalso)
			if ferr != nil {
				vivo.Fechar()
				cancelar()
				t.Fatalf("%s/%s: o falso nao deu a forma: %v", prefixo, nome, ferr)
			}
			medida["pedidos_no_fornecedor"] = depois.Pedidos - antes.Pedidos
			medida["partes_no_fornecedor"] = aos516Diferenca(antes.Partes, depois.Partes)
			medida["recusas_do_falso"] = aos516Diferenca(antes.Recusas, depois.Recusas)
			medida["media_no_fornecedor"] = depois.Media[len(antes.Media):]
			antes = depois
			return medida
		}
		// O controlo: um pedido só com texto tem de chegar ao falso. Se não chegar, o ensaio não
		// mediu nada e o teste fica vermelho.
		controlo := enviar("controlo_texto", []byte(`{"model":"`+AliasDaRota+`","max_tokens":64,"messages":[{"role":"user","content":"`+TextoDaSonda+`"}]}`))
		if controlo["pedidos_no_fornecedor"] != 1 || controlo["http"] != http.StatusOK {
			vivo.Fechar()
			cancelar()
			t.Fatalf("%s: o controlo so com texto nao chegou ao falso: %v", prefixo, controlo)
		}
		doPrefixo["controlo_texto"] = controlo
		for _, v := range videos {
			soma := sha256.Sum256(v.bytes)
			medida := enviar(v.nome, aos534Pedido(AliasDaRota, v.bytes))
			medida["bytes_do_video"], medida["sha256_enviado"] = len(v.bytes), hex.EncodeToString(soma[:])
			medida["resultado"] = aos534Resultado(medida, hex.EncodeToString(soma[:]), len(v.bytes))
			doPrefixo["video_"+v.nome] = medida
			t.Logf("%s/%s: %s", prefixo, v.nome, medida["resultado"])
		}
		vivo.Fechar()
		cancelar()
		medidas[prefixo] = doPrefixo
	}
	resumo, _ := json.Marshal(medidas)
	for _, segredo := range append(mestras, chave, "Bearer ") {
		if segredo != "" && strings.Contains(string(resumo), segredo) {
			t.Fatalf("o relatorio deixa sair um segredo")
		}
	}
	t.Logf("AOS_BANCO_VIDEO_REPORT %s", resumo)
}

// Os resultados do spike, por rota e por tamanho ([aos534Resultado]). Vocabulário fechado.
const (
	// aos534Intacta — chegou ao fornecedor uma parte `video_url` com `data:video/mp4;base64,` e
	// os mesmos bytes.
	aos534Intacta = "intacta"
	// aos534RecusadaPeloProxy — o proxy não respondeu 200 e nada chegou ao fornecedor.
	aos534RecusadaPeloProxy = "recusada_pelo_proxy"
	// aos534AlteradaPeloProxy — chegou ao fornecedor, mas com outro tipo, outra classe de URL ou
	// outros bytes; ou não chegou parte nenhuma com media.
	aos534AlteradaPeloProxy = "alterada_pelo_proxy"
	// aos534RecusadaPeloFalso — chegou ao fornecedor e o falso recusou-a (o tecto dele, por exemplo).
	aos534RecusadaPeloFalso = "recusada_pelo_falso"
)

// aos534Resultado classifica uma medida pelo que chegou ao fornecedor.
func aos534Resultado(medida map[string]any, sha string, tamanho int) string {
	n, _ := medida["pedidos_no_fornecedor"].(int)
	if n == 0 {
		return aos534RecusadaPeloProxy
	}
	if recusas, _ := medida["recusas_do_falso"].(map[string]int); len(recusas) > 0 {
		return aos534RecusadaPeloFalso
	}
	media, _ := medida["media_no_fornecedor"].([]MediaNoFornecedor)
	for _, m := range media {
		if m.Parte == "video_url" && m.Classe == ClasseDataURLVideoMP4 && m.Sha256 == sha && m.Bytes == tamanho {
			return aos534Intacta
		}
	}
	return aos534AlteradaPeloProxy
}
