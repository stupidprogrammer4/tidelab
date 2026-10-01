package routers

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stupidprogrammer4/tidelab/internal/modules/system/services"
)

func TestHealthReportsProcessOnly(t *testing.T) {
	app := fiber.New()
	Register(app, services.InfoService{})
	response, err := app.Test(httptest.NewRequest("GET", "/healthz", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"status":"ok"}` {
		t.Fatalf("body = %q", got)
	}
}
