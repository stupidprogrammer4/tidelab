package routers

import (
	"github.com/gofiber/fiber/v3"

	"github.com/stupidprogrammer4/tidelab/internal/modules/system/domain"
)

type HealthProvider interface {
	Health() domain.Health
}

func Register(app *fiber.App, service HealthProvider) {
	app.Get("/healthz", func(c fiber.Ctx) error {
		return c.JSON(service.Health())
	})
}
