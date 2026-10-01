package services

import "github.com/stupidprogrammer4/tidelab/internal/modules/system/domain"

const Version = "0.1.0-dev"

type InfoService struct{}

func (InfoService) Version() domain.Version {
	return domain.Version{Name: "TideLab", Version: Version}
}

func (InfoService) Health() domain.Health {
	return domain.Health{Status: "ok"}
}
