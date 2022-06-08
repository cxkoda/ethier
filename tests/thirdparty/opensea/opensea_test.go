package opensea

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"

	"github.com/divergencetech/ethier/ethtest"
	"github.com/divergencetech/ethier/ethtest/openseatest"
	"github.com/divergencetech/ethier/ethtest/revert"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/h-fam/errdiff"
)

const (
	deployer = iota
	proxy
	newOwner
	vandal
	recipient0
	recipient1
	recipient2

	numAccounts
)

func deploy(t *testing.T, numOptions int64, baseOptionURI string) (*ethtest.SimulatedBackend, *TestableOpenSeaMintable, *OpenSeaERC721Factory) {
	sim := ethtest.NewSimulatedBackendTB(t, numAccounts)

	openseatest.DeployProxyRegistryTB(t, sim)
	openseatest.SetProxyTB(t, sim, sim.Addr(deployer), sim.Addr(proxy))

	_, _, nft, err := DeployTestableOpenSeaMintable(
		sim.Acc(deployer), sim,
		big.NewInt(numOptions),
		baseOptionURI,
	)
	if err != nil {
		t.Fatalf("DeployTestableOpenSeaMintable(%d, %q) error %v", numOptions, baseOptionURI, err)
	}

	addr, err := nft.Factory(nil)
	if err != nil {
		t.Fatalf("%T.Factory() error %v", nft, err)
	}
	factory, err := NewOpenSeaERC721Factory(addr, sim)
	if err != nil {
		t.Fatalf("NewOpenSeaERC721Factory([address from TestableOpenSeaMintable]) error %v", err)
	}

	return sim, nft, factory
}

func TestFactoryReadOnly(t *testing.T) {
	const (
		numOptions = 5
		baseURI    = "option/"
	)
	sim, nft, factory := deploy(t, numOptions, baseURI)

	t.Run("numOptions propagated from primary contract", func(t *testing.T) {
		got, err := factory.NumOptions(nil)
		if want := big.NewInt(numOptions); err != nil || got.Cmp(want) != 0 {
			t.Errorf("%T.NumOptions() got %d, err = %v; want %t, nil err", factory, got, err, want)
		}
	})

	t.Run("canMint propagated from primary contract", func(t *testing.T) {
		for i := int64(0); i < numOptions+5; i++ {
			can := i%2 == 0
			sim.Must(t, "SetCanMint(%d, %t)", i, can)(nft.SetCanMint(sim.Acc(deployer), big.NewInt(i), can))

			// Although set on the NFT contract, test that the factory
			// propagates this but also limits to the number of available
			// options regardless of what the primary contract returns.
			got, err := factory.CanMint(nil, big.NewInt(i))
			if want := can && i < numOptions; err != nil || got != want {
				t.Errorf("%T.CanMint(%d) after setting on primary contract; got %t, err = %v; want %t, nil err", factory, i, got, err, want)
			}
		}
	})

	t.Run("option URI", func(t *testing.T) {
		for i := int64(0); i < 3; i++ {
			want := fmt.Sprintf("%s%d", baseURI, i)
			got, err := factory.TokenURI(nil, big.NewInt(i))
			if err != nil || got != want {
				t.Errorf("%T.TokenURI(%d) got %q, err = %v; want %q, nil err", factory, i, got, err, want)
			}
		}
	})

	t.Run("ownership transferred from deploying contract", func(t *testing.T) {
		want := sim.Addr(deployer)
		got, err := factory.Owner(nil)
		if err != nil || !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Errorf("%T.Owner() got %v, err = %v; want %v (deploying address, not primary contract), nil err", factory, got, err, want)
		}
	})
}

func TestTransferEvents(t *testing.T) {
	const numOptions = 5
	sim, _, factory := deploy(t, numOptions, "")

	sim.Must(t, "TransferOwnership()")(factory.TransferOwnership(sim.Acc(deployer), sim.Addr(newOwner)))

	iter, err := factory.FilterTransfer(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("%T.FilterTransfer(nil, nil, nil, nil) error %v", factory, err)
	}
	defer iter.Close()

	var got, want []*OpenSeaERC721FactoryTransfer
	for iter.Next() {
		got = append(got, iter.Event)
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("%T.Error(): %v", iter, err)
	}

	// Contract deployment must trigger a single Transfer event per option, from
	// the zero address to the EOA deployer of the contracts (not to the NFT
	// contract that actually deploys the factory).
	for i := int64(0); i < numOptions; i++ {
		want = append(want, &OpenSeaERC721FactoryTransfer{
			From:    common.Address{}, // initially from address(0),
			To:      sim.Addr(deployer),
			TokenId: big.NewInt(i),
		})
	}
	// Ownership transfer must also result in Transfer events to the new owner.
	for i := int64(0); i < numOptions; i++ {
		want = append(want, &OpenSeaERC721FactoryTransfer{
			From:    sim.Addr(deployer),
			To:      sim.Addr(newOwner),
			TokenId: big.NewInt(i),
		})
	}

	ignore := ethtest.Comparers(cmpopts.IgnoreFields(OpenSeaERC721FactoryTransfer{}, "Raw"))

	if diff := cmp.Diff(want, got, ignore...); diff != "" {
		t.Errorf("After %T deployment and single ownership transfer; Transfer events diff (-want +got):\n%s", factory, diff)
	}
}

func TestMint(t *testing.T) {
	const numOptions = 3
	sim, nft, factory := deploy(t, numOptions, "")

	for i := int64(0); i < numOptions; i++ {
		sim.Must(t, "SetCanMint(%d, true)", i)(nft.SetCanMint(sim.Acc(deployer), big.NewInt(i), true))
	}

	const onlyFactoryRevert = "OpenSeaERC721Mintable: only factory"

	tests := []struct {
		name           string
		contract       interface{} // only for error reporting
		mint           func(*bind.TransactOpts, *big.Int, common.Address) (*types.Transaction, error)
		mintAs         *bind.TransactOpts
		mintOption     int64
		mintTo         common.Address
		errDiffAgainst string
	}{
		{
			name:           "factory.Mint() as end recipient",
			contract:       factory,
			mint:           factory.Mint,
			mintAs:         sim.Acc(recipient0),
			errDiffAgainst: "OpenSeaERC721Factory: only owner or proxy",
		},
		{
			name:           "nft.FactoryMint() as owner instead of factory",
			contract:       nft,
			mint:           nft.FactoryMint,
			mintAs:         sim.Acc(deployer),
			errDiffAgainst: onlyFactoryRevert,
		},
		{
			name:           "nft.FactoryMint() as owner's Wyvern proxy instead of factory",
			contract:       nft,
			mint:           nft.FactoryMint,
			mintAs:         sim.Acc(proxy),
			errDiffAgainst: onlyFactoryRevert,
		},
		{
			name:       "factory.Mint() as owner",
			contract:   factory,
			mint:       factory.Mint,
			mintAs:     sim.Acc(deployer),
			mintOption: 1,
			mintTo:     sim.Addr(recipient0),
		},
		{
			name:       "factory.Mint() as owner's Wyvern proxy",
			contract:   factory,
			mint:       factory.Mint,
			mintAs:     sim.Acc(proxy),
			mintOption: 2,
			mintTo:     sim.Addr(recipient1),
		},
		{
			// This is how OpenSea has hacked the ERC721 standard + Wyvern
			// protocol to mint directly on their site. A standard listing of a
			// factory token results in a call to transferFrom(owner, buyer)
			// that results in a new mint for the buyer.
			name:     "factory.transferFrom() propagates to mint()",
			contract: factory,
			mint: func(opts *bind.TransactOpts, optionID *big.Int, to common.Address) (*types.Transaction, error) {
				return factory.TransferFrom(opts, common.Address{}, to, optionID)
			},
			mintAs:     sim.Acc(proxy),
			mintOption: 3,
			mintTo:     sim.Addr(recipient2),
		},
	}

	// Value will be checked iff all tests pass so we know that the contract is
	// in the correct state.
	wantMinted := []TestableOpenSeaMintableMint{
		{
			OptionId: big.NewInt(1),
			To:       sim.Addr(recipient0),
		},
		{
			OptionId: big.NewInt(2),
			To:       sim.Addr(recipient1),
		},
		{
			OptionId: big.NewInt(3),
			To:       sim.Addr(recipient2),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.mint(tt.mintAs, big.NewInt(tt.mintOption), tt.mintTo)
			if diff := errdiff.Check(err, tt.errDiffAgainst); diff != "" {
				t.Errorf("%T.[Factory]Mint() %s", tt.contract, diff)
			}
		})
	}

	if t.Failed() {
		return
	}

	n, err := nft.NumMinted(nil)
	if err != nil {
		t.Fatalf("%T.NumMinted() error %v", nft, err)
	}
	if !n.IsInt64() {
		t.Fatalf("%T.NumMinted().IsInt64() got false; want true", nft)
	}

	var gotMinted []TestableOpenSeaMintableMint
	for i := int64(0); i < n.Int64(); i++ {
		got, err := nft.Mints(nil, big.NewInt(i))
		if err != nil {
			t.Fatalf("%T.Mints(%d) error %v", nft, i, err)
		}
		gotMinted = append(gotMinted, got)
	}

	if diff := cmp.Diff(wantMinted, gotMinted, ethtest.Comparers()...); diff != "" {
		t.Errorf("All %T.Mints() after successful and blocked mints; (-want +got) diff:\n%s", nft, diff)
	}
}

func TestMintPausing(t *testing.T) {
	sim, _, factory := deploy(t, 1, "")

	mint := func() (*types.Transaction, error) {
		return factory.Mint(sim.Acc(deployer), big.NewInt(0), sim.Addr(recipient0))
	}

	sim.Must(t, "factory.Pause()")(factory.Pause(sim.Acc(deployer)))
	if diff := revert.Paused.Diff(mint()); diff != "" {
		t.Errorf("%T.Mint() when paused; %s", factory, diff)
	}

	sim.Must(t, "factory.Unpause()")(factory.Unpause(sim.Acc(deployer)))
	if _, err := mint(); err != nil {
		t.Errorf("%T.Mint() when not paused; error %v", factory, err)
	}
}

func TestIsApprovedForAll(t *testing.T) {
	sim, _, factory := deploy(t, 1, "")

	tests := []struct {
		owner, operator common.Address
		want            bool
	}{
		{
			owner:    sim.Addr(deployer),
			operator: sim.Addr(deployer),
			want:     true,
		},
		{
			owner:    sim.Addr(deployer),
			operator: sim.Addr(proxy),
			want:     true,
		},
		{
			owner:    sim.Addr(deployer),
			operator: sim.Addr(vandal),
			want:     false,
		},
		{
			owner:    sim.Addr(vandal),
			operator: sim.Addr(deployer),
			want:     false,
		},
		{
			owner:    sim.Addr(proxy),
			operator: sim.Addr(deployer),
			want:     false,
		},
		{
			owner:    sim.Addr(proxy),
			operator: sim.Addr(proxy),
			want:     false,
		},
	}

	t.Logf("Contract owner: %v", sim.Addr(deployer))
	t.Logf("Contract owner's proxy: %v", sim.Addr(proxy))
	t.Logf("Evil miscreant: %v", sim.Addr(vandal))

	for _, tt := range tests {
		got, err := factory.IsApprovedForAll(nil, tt.owner, tt.operator)
		if err != nil || got != tt.want {
			t.Errorf("%T.IsApprovedForAll(%v, %v) got %t, err = %v; want %t, nil err", factory, tt.owner, tt.operator, got, err, tt.want)
		}
	}
}
