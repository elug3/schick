package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/elug3/dupli1/product/pkg/domain"
	"github.com/elug3/dupli1/product/pkg/ports"
	"github.com/jackc/pgx/v4"
)

const (
	mockEcoBagBrandCode = "DUP"
	mockEcoBagBrandName = "Dupli1 Studio"
	mockEcoBagStyleCode = "ECO01"
	mockEcoBagStyleName = "에코백"
	mockEcoBagName      = "에코백"
	mockEcoBagDesc      = "캔버스 에코백. Dupli1 Studio 오리지널 목업 상품."
	mockEcoBagMaterial  = "Canvas"
	mockEcoBagPrice     = 100
	mockEcoBagStock     = 50
)

// SeedMockEcoBag ensures the Dupli1 Studio mock eco-bag exists at 100 KRW
// with sellable stock. Idempotent: DUP/ECO01 is created once, then price,
// status, and stock are reconciled on later boots.
func (s *ProductSearchStore) SeedMockEcoBag(ctx context.Context, stock *InventoryStore) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("product store not initialized")
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO brands (code, name) VALUES ($1, $2) ON CONFLICT (code) DO NOTHING`,
		mockEcoBagBrandCode, mockEcoBagBrandName,
	); err != nil {
		return fmt.Errorf("brand: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO styles (brand_code, code, name) VALUES ($1, $2, $3)
		 ON CONFLICT (brand_code, code) DO NOTHING`,
		mockEcoBagBrandCode, mockEcoBagStyleCode, mockEcoBagStyleName,
	); err != nil {
		return fmt.Errorf("style: %w", err)
	}

	id, err := s.mockEcoBagProductID(ctx)
	if err != nil {
		return err
	}
	if id == "" {
		created, err := s.CreateProduct(ctx, domain.Product{
			Name:        mockEcoBagName,
			Description: mockEcoBagDesc,
			Brand:       mockEcoBagBrandName,
			BrandCode:   mockEcoBagBrandCode,
			StyleCode:   mockEcoBagStyleCode,
			Material:    mockEcoBagMaterial,
			Category:    "bags",
			SubCategory: "tote",
			Style:       "casual",
			Target:      "all",
			Price:       mockEcoBagPrice,
			Status:      "active",
			Color:       "Black",
			Tags:        []string{"mock"},
		})
		if err != nil {
			if !errors.Is(err, ports.ErrConflict) {
				return fmt.Errorf("create: %w", err)
			}
			id, err = s.mockEcoBagProductID(ctx)
			if err != nil {
				return err
			}
			if id == "" {
				return fmt.Errorf("create conflict but product not found")
			}
		} else {
			id = created.ID
		}
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE products
		   SET name = $2,
		       description = $3,
		       price = $4,
		       status = 'active',
		       category = 'bags',
		       sub_category = 'tote',
		       bag_style = 'casual',
		       target = 'all',
		       material = $5,
		       updated_at = NOW()
		 WHERE id = $1`,
		id, mockEcoBagName, mockEcoBagDesc, mockEcoBagPrice, mockEcoBagMaterial,
	); err != nil {
		return fmt.Errorf("update: %w", err)
	}

	return s.ensureMockEcoBagStock(ctx, stock, id)
}

func (s *ProductSearchStore) mockEcoBagProductID(ctx context.Context) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM products WHERE brand_code = $1 AND style_code = $2`,
		mockEcoBagBrandCode, mockEcoBagStyleCode,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup: %w", err)
	}
	return id, nil
}

func (s *ProductSearchStore) ensureMockEcoBagStock(ctx context.Context, stock *InventoryStore, productID string) error {
	if stock == nil {
		return nil
	}
	variants, err := s.ListVariants(ctx, productID)
	if err != nil {
		return fmt.Errorf("list variants: %w", err)
	}
	now := time.Now()
	for _, v := range variants {
		if _, err := stock.SetQuantity(ctx, v.SkuID, mockEcoBagStock, now); err == nil {
			continue
		} else if !errors.Is(err, ports.ErrInventoryItemNotFound) {
			return fmt.Errorf("set stock %s: %w", v.SKU, err)
		}
		if err := stock.EnsureItem(ctx, v.SkuID, v.SKU, now); err != nil {
			return fmt.Errorf("ensure stock %s: %w", v.SKU, err)
		}
		if _, err := stock.SetQuantity(ctx, v.SkuID, mockEcoBagStock, now); err != nil {
			return fmt.Errorf("set stock %s: %w", v.SKU, err)
		}
	}
	return nil
}
