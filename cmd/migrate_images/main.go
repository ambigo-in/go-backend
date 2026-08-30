package main

import (
	"context"
	"fmt"

	"ambigo-backend/config"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	appConfig := config.LoadConfig()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appConfig.DatabaseURL)
	if err != nil {
		logger.Log.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer pool.Close()

	gcsSvc, err := storage.NewStorageService(ctx, appConfig.GCSBucketName)
	if err != nil {
		logger.Log.Fatal().Err(err).Msg("Failed to initialize GCS storage service")
	}

	logger.Log.Info().Str("bucket", appConfig.GCSBucketName).Msg("Starting document image migration to GCS...")

	rows, err := pool.Query(ctx, `SELECT id::text, portrait_image, poi_image, dl_image, rc_image, amb_front, amb_inside FROM unverified_drivers`)
	if err != nil {
		logger.Log.Fatal().Err(err).Msg("Failed to query unverified_drivers")
	}
	defer rows.Close()

	migratedCount := 0
	for rows.Next() {
		var id, portrait, poi, dl, rc, ambFront, ambInside string
		if err := rows.Scan(&id, &portrait, &poi, &dl, &rc, &ambFront, &ambInside); err != nil {
			logger.Log.Error().Err(err).Msg("Failed to scan row")
			continue
		}

		pURL, _ := gcsSvc.UploadBase64IfImage(ctx, fmt.Sprintf("drivers/%s/portrait.jpg", id), portrait)
		poiURL, _ := gcsSvc.UploadBase64IfImage(ctx, fmt.Sprintf("drivers/%s/poi.jpg", id), poi)
		dlURL, _ := gcsSvc.UploadBase64IfImage(ctx, fmt.Sprintf("drivers/%s/dl.jpg", id), dl)
		rcURL, _ := gcsSvc.UploadBase64IfImage(ctx, fmt.Sprintf("drivers/%s/rc.jpg", id), rc)
		afURL, _ := gcsSvc.UploadBase64IfImage(ctx, fmt.Sprintf("drivers/%s/amb_front.jpg", id), ambFront)
		aiURL, _ := gcsSvc.UploadBase64IfImage(ctx, fmt.Sprintf("drivers/%s/amb_inside.jpg", id), ambInside)

		_, err = pool.Exec(ctx, `UPDATE unverified_drivers SET portrait_image=$1, poi_image=$2, dl_image=$3, rc_image=$4, amb_front=$5, amb_inside=$6 WHERE id=$7::uuid`,
			pURL, poiURL, dlURL, rcURL, afURL, aiURL, id)
		if err != nil {
			logger.Log.Error().Err(err).Str("id", id).Msg("Failed to update driver images in DB")
		} else {
			migratedCount++
		}
	}

	logger.Log.Info().Int("count", migratedCount).Msg("Successfully completed GCS image migration!")
}
