package bancoensaio

import (
	"bytes"
	"errors"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"testing/fstest"
)

// AOS-529 — A BATERIA DE OCR.

// aos529DigestDaBateriaOCR é o digest da bateria de OCR 1.0.0: o ficheiro dos casos e os píxeis
// das imagens. Mudar um byte de um caso, a fonte ou uma degradação muda-o, e é outra bateria.
const aos529DigestDaBateriaOCR = "sha256:390f70c806d6c5b050587707e3a2d9974e1bf2fbe3812424b0a82a864034c703"

func aos529Bateria(t *testing.T) *BateriaOCR {
	t.Helper()
	b, err := CarregarBateriaOCR()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAOS529_BateriaOCR_TemDigestProprioEAsOutrasMantemOSeu(t *testing.T) {
	b := aos529Bateria(t)
	if b.Versao != "1.0.0" || b.Digest() != aos529DigestDaBateriaOCR {
		t.Errorf("bateria de OCR: versao %s, digest %s (quer %s)", b.Versao, b.Digest(), aos529DigestDaBateriaOCR)
	}
	outra := aos529Bateria(t)
	if outra.Digest() != b.Digest() {
		t.Error("a mesma bateria deu dois digests")
	}
	for i, m := range b.Media() {
		if !bytes.Equal(m.Bytes, outra.Media()[i].Bytes) || m.Digest != outra.Media()[i].Digest {
			t.Errorf("a imagem %s nao e determinista", m.ID)
		}
	}
	um, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	if um.Digest() != aos525DigestDaBateria1 || aos525Bateria2(t).Digest() != aos525DigestDaBateria2 || aos528Bateria3(t).Digest() != aos528DigestDaBateria3 {
		t.Error("as baterias 1.0.0, 2.0.0 e 3.0.0 mudaram")
	}
}

func TestAOS529_BateriaOCR_CobreAcentosDegradacoesEFormatos(t *testing.T) {
	b := aos529Bateria(t)
	com, sem := 0, 0
	for _, d := range b.Documentos {
		if d.ComAcentos() {
			com++
		} else {
			sem++
		}
	}
	if com < 2 || sem < 2 {
		t.Errorf("quer pelo menos dois documentos com acentos e dois sem: %d e %d", com, sem)
	}
	degradacoes, formatos, tabela := map[string]bool{}, map[string]bool{}, false
	for _, d := range b.Documentos {
		tabela = tabela || len(d.Tabela) > 0
	}
	for _, m := range b.Media() {
		degradacoes[m.Degradacao], formatos[m.Formato] = true, true
		switch m.Formato {
		case FormatoPNG:
			img, err := png.Decode(bytes.NewReader(m.Bytes))
			if err != nil || img.Bounds().Dx() != m.Largura || img.Bounds().Dy() != m.Altura {
				t.Errorf("%s: PNG ilegivel ou com outras dimensoes: %v", m.ID, err)
			}
		case FormatoJPEG:
			if _, err := jpeg.Decode(bytes.NewReader(m.Bytes)); err != nil {
				t.Errorf("%s: JPEG ilegivel: %v", m.ID, err)
			}
		case FormatoPDF:
			s := string(m.Bytes)
			if !strings.HasPrefix(s, "%PDF-1.4\n") || !strings.Contains(s, "/Subtype /Image") || !strings.HasSuffix(s, "%%EOF\n") {
				t.Errorf("%s: nao e um PDF de uma pagina com uma imagem", m.ID)
			}
		}
		if !strings.HasPrefix(m.textoDeOrigem, b.Marca+"\n") {
			t.Errorf("%s: o texto de origem nao abre pela marca", m.ID)
		}
	}
	if len(degradacoes) != 3 || len(formatos) != 3 || !tabela {
		t.Errorf("quer as tres degradacoes, os tres formatos e uma tabela: %v %v %v", degradacoes, formatos, tabela)
	}
}

func aos529Casos(substituir func(string) string) fstest.MapFS {
	cru, _ := ficheirosDaBateriaOCR.ReadFile(pastaDaBateriaOCR + "/casos.json")
	return fstest.MapFS{"b/casos.json": &fstest.MapFile{Data: []byte(substituir(string(cru)))}}
}

func TestAOS529_BateriaOCR_OCarregamentoEFailClosed(t *testing.T) {
	if _, err := carregarBateriaOCR(aos529Casos(func(s string) string { return s }), "b"); err != nil {
		t.Fatalf("a copia intacta tem de carregar: %v", err)
	}
	casos := map[string]func(string) string{
		"sem ficheiro":       func(string) string { return "" },
		"campo desconhecido": func(s string) string { return strings.Replace(s, `"versao"`, `"versao_x": 1, "versao"`, 1) },
		"sem marca na 1.a linha": func(s string) string {
			return strings.Replace(s, `"linhas": [`+"\n        \"DOCUMENTO SINTETICO AOS-529\",", `"linhas": [`, 1)
		},
		"facto fora do texto": func(s string) string {
			return strings.Replace(s, `"Tecidos do Norte", "509876123"`, `"Tecidos do Norte", "509876124"`, 1)
		},
		"letra que a fonte nao tem": func(s string) string {
			return strings.Replace(s, "NIF 509876123", "NIF 509876123 €", 1)
		},
		"degradacao fora do vocabulario": func(s string) string {
			return strings.Replace(s, `"degradacao": "limpo"`, `"degradacao": "dobrado"`, 1)
		},
		"formato fora do vocabulario": func(s string) string { return strings.Replace(s, `"formato": "jpeg"`, `"formato": "tiff"`, 1) },
		"documento inexistente":       func(s string) string { return strings.Replace(s, `"documento": "recibo"`, `"documento": "talao"`, 1) },
		"imagem repetida": func(s string) string {
			return strings.Replace(s, `"id": "fatura-a-ruido"`, `"id": "fatura-a-limpo"`, 1)
		},
		"tabela irregular":  func(s string) string { return strings.Replace(s, `"48", "9,10"]`, `"48"]`, 1) },
		"facto com espacos": func(s string) string { return strings.Replace(s, `"Tecidos do Norte"`, `" Tecidos do Norte"`, 1) },
	}
	for nome, mudar := range casos {
		t.Run(nome, func(t *testing.T) {
			var origem fstest.MapFS
			if nome == "sem ficheiro" {
				origem = fstest.MapFS{}
			} else {
				origem = aos529Casos(mudar)
				if string(origem["b/casos.json"].Data) == string(aos529Casos(func(s string) string { return s })["b/casos.json"].Data) {
					t.Fatal("a mutacao nao mudou nada")
				}
			}
			if _, err := carregarBateriaOCR(origem, "b"); !errors.Is(err, ErrBateria) {
				t.Errorf("quer ErrBateria, deu %v", err)
			}
		})
	}
}

func TestAOS529_AFonteDesenhaAsLetrasDosDocumentosPortugueses(t *testing.T) {
	for _, r := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789 .,:-/()%áàâãéêíóôõúçÁÀÂÃÉÊÍÓÔÕÚÇ" {
		if _, ok := glifo(r); !ok {
			t.Errorf("a fonte nao desenha %q", r)
		}
	}
	a, _ := glifo('a')
	aa, _ := glifo('á')
	if a == aa {
		t.Error("o acento nao se ve")
	}
	if _, ok := glifo('€'); ok {
		t.Error("a fonte nao tem o euro e nao o pode fingir")
	}
}

func TestAOS529_RegraComAcentos(t *testing.T) {
	if !factoNaSaida("Morada: Evora", "Évora") || factoComAcentosNaSaida("Morada: Evora", "Évora") {
		t.Error("a regra perdoa o acento; a comparacao com acentos nao")
	}
	if !factoComAcentosNaSaida("MORADA:  ÉVORA", "Évora") || !factoComAcentosNaSaida("CX‑208", "CX-208") {
		t.Error("a comparacao com acentos dobra maiusculas, espacos e hifenes como a regra")
	}
	if dobrarParaComparar("Panificação") != "panificacao" {
		t.Error("a regra dos factos mudou")
	}
}
