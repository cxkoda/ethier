// SPDX-License-Identifier: MIT
// Copyright (c) 2021 the ethier authors (github.com/divergencetech/ethier)
pragma solidity >=0.8.0 <0.9.0;

/// @notice The struct containing GIF settings and raw data
struct GIF {
    bytes raw;
    uint128 pos;
    uint16 width;
    uint16 height;
    uint16 numFrames;
    uint8 lzwBits;
}

/// @notice An encoder library to handle the assembly of animated GIFs.
/// @dev The library is intended to be used in the following order:
///      1) Preallocate buffer using `init`.
///      2) Add frames using `addFrameUncompressed`.
///      3) Finalize the buffer using `finalize`.
///      The RGB color depth is 8bit by convetion.
library GIFEncoder {
    /// @notice Generates a grayscale colormaps with `numColors` levels.
    function getGrayscaleColormap(uint16 numColors)
        external
        pure
        returns (bytes memory colormap)
    {
        colormap = new bytes(numColors * 3);

        uint128 pos = 0;
        for (uint16 idx; idx < numColors; ++idx) {
            bytes1 tmp = bytes1(uint8((idx * 256) / numColors));
            colormap[pos++] = tmp;
            colormap[pos++] = tmp;
            colormap[pos++] = tmp;
        }
    }

    /// @notice Initializes buffer space for the GIF image
    function init(
        uint16 width,
        uint16 height,
        uint16 numFrames,
        bytes memory colormap
    ) external pure returns (GIF memory gif) {
        require(colormap.length % 3 == 0);

        // TODO: Make this general
        // This is currently required by the GCT copy algorithm
        require(colormap.length % 32 == 0);

        uint256 numSubBlocks = (width * height) / FRAME_BYTES_PER_SUBBLOCK;
        if ((width * height) % FRAME_BYTES_PER_SUBBLOCK > 0) {
            numSubBlocks += 1;
        }
        uint256 size = ((FRAME_BYTES_PER_SUBBLOCK + 2) * numSubBlocks + 22) *
            numFrames +
            417;
        gif.raw = new bytes(size);

        gif.height = height;
        gif.width = width;
        gif.numFrames = numFrames;

        // Detemine the LZW width
        {
            uint256 numColors = colormap.length / 3;
            uint8 addBit;
            uint8 lzwWidth = 0;
            assembly {
                for {

                } gt(numColors, 1) {
                    numColors := shr(1, numColors)
                    lzwWidth := add(lzwWidth, 1)
                } {
                    if and(numColors, 0x1) {
                        addBit := 1
                    }
                    {

                    }
                }
            }
            gif.lzwBits = lzwWidth + addBit;
        }

        // The current position in the raw buffer.
        uint128 pos = 0;

        // Header
        gif.raw[pos++] = 0x47;
        gif.raw[pos++] = 0x49;
        gif.raw[pos++] = 0x46;
        gif.raw[pos++] = 0x38;
        gif.raw[pos++] = 0x39;
        gif.raw[pos++] = 0x61;

        // Logical screen width
        gif.raw[pos++] = bytes1(uint8(width));
        gif.raw[pos++] = bytes1(uint8(width >> 8));

        // Logical screen heigth
        gif.raw[pos++] = bytes1(uint8(height));
        gif.raw[pos++] = bytes1(uint8(height >> 8));

        // GCT settings
        gif.raw[pos++] = 0xf6;

        // Background color
        gif.raw[pos++] = 0x00;

        // Default pixel ratio
        gif.raw[pos++] = 0x00;

        // Global Color Table (GCT)
        // Copy colormap
        {
            uint256 numChunks = colormap.length / 32;
            uint256 offset;
            uint256 colormapOffset;
            assembly {
                offset := add(mload(gif), add(0x20, pos))
                colormapOffset := add(colormap, 0x20)
            }

            for (uint256 chunk = 0; chunk < numChunks; ++chunk) {
                assembly {
                    mstore(offset, mload(colormapOffset))
                    pos := add(pos, 0x20)
                    offset := add(offset, 0x20)
                    colormapOffset := add(colormapOffset, 0x20)
                }
            }
        }

        // Application extension
        {
            uint256 AE = 0x21FF0B4E45545343415045322E300301000000 << 104;
            assembly {
                mstore(add(mload(gif), add(0x20, pos)), AE)
            }
            pos += 19;
        }

        gif.pos = pos;
    }

    /// @notice Finalizes the GIF buffer.
    /// @dev This adds the end-of-file trailer and trims the buffer size.
    ///      This has to be called last.
    function finalize(GIF memory gif) internal pure {
        // Get the current position in the GIF buffer
        uint128 pos = gif.pos;

        // Trailer
        gif.raw[pos++] = 0x3B;

        // Trim free buffer space
        assembly {
            mstore(mload(gif), pos)
        }
    }

    /// @dev Frame data is organized as subblocks within GIFs. We use 32B for
    ///      maximum copy efficiency.
    uint8 private constant FRAME_BYTES_PER_SUBBLOCK = 32;

    /// @dev Each subblock contains an additional byte for length.
    ///      Therefore each subblock has length FRAME_BYTES_PER_SUBBLOCK + 1.
    uint8 private constant BYTES_PER_SUBBLOCK = 33;

    /// @notice Adds an uncompressed frame to the GIF.
    function addFrameUncompressed(GIF memory gif, bytes memory frame)
        internal
        pure
    {
        // TODO Make this general
        // Currently required by the frame copy algorith,
        require((gif.width * gif.height) % FRAME_BYTES_PER_SUBBLOCK == 0);

        require(frame.length == gif.width * gif.height);

        // Get the current position in the GIF buffer
        uint128 pos = gif.pos;

        // Graphics Control Extension
        gif.raw[pos++] = 0x21;
        gif.raw[pos++] = 0xF9;

        // Num bytes following
        gif.raw[pos++] = 0x04;

        // Bit field: First 3 reserved,
        // 3 Disposal method (001 = don't dispose),
        // 1 User input,
        // 1 Transparent color (0 = none)
        gif.raw[pos++] = 0x04;

        // Animation delay
        gif.raw[pos++] = 0x04;
        gif.raw[pos++] = 0x00;

        // transparent color in GCT (FF disabled)
        gif.raw[pos++] = 0xff;

        // GCE end
        gif.raw[pos++] = 0x00;

        // Image
        gif.raw[pos++] = 0x2C;

        // north-west corner
        gif.raw[pos++] = 0x00;
        gif.raw[pos++] = 0x00;
        gif.raw[pos++] = 0x00;
        gif.raw[pos++] = 0x00;

        // Image width
        gif.raw[pos++] = bytes1(uint8(gif.width));
        gif.raw[pos++] = bytes1(uint8(gif.width >> 8));

        // Image heigth
        gif.raw[pos++] = bytes1(uint8(gif.height));
        gif.raw[pos++] = bytes1(uint8(gif.height >> 8));

        // Local color table
        gif.raw[pos++] = 0x00;

        // LZW encoding length
        gif.raw[pos++] = bytes1(gif.lzwBits);

        {
            uint256 nSubBlocks = frame.length / FRAME_BYTES_PER_SUBBLOCK;
            uint256 offset;
            uint256 frameOffset;
            assembly {
                offset := add(mload(gif), add(0x20, pos))
                frameOffset := add(frame, 0x20)

                for {
                    let subBlock := 0
                } lt(subBlock, nSubBlocks) {
                    subBlock := add(subBlock, 1)
                } {
                    // Amount of bytes in subblock
                    mstore8(offset, BYTES_PER_SUBBLOCK)
                    // Clear
                    offset := add(offset, 1)
                    mstore8(offset, 0x80)

                    offset := add(offset, 1)
                    mstore(offset, mload(frameOffset))
                    offset := add(offset, 0x20)
                    frameOffset := add(frameOffset, 0x20)
                    pos := add(pos, 0x22)
                }
            }
        }

        // Stop
        gif.raw[pos++] = 0x01;
        gif.raw[pos++] = 0x81;

        // End of image block
        gif.raw[pos++] = 0x00;

        // Store back the buffer position
        gif.pos = pos;
    }
}
