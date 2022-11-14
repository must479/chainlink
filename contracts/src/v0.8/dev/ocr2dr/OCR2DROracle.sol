// SPDX-License-Identifier: MIT
pragma solidity ^0.8.6;

import "../../interfaces/TypeAndVersionInterface.sol";
import "../interfaces/OCR2DRClientInterface.sol";
import "../interfaces/OCR2DROracleInterface.sol";
import "./OCR2DRBillableAbstract.sol";
import "../ocr2/OCR2Base.sol";

/**
 * @title OCR2DR oracle contract
 * @dev THIS CONTRACT HAS NOT GONE THROUGH ANY SECURITY REVIEW. DO NOT USE IN PROD.
 */
contract OCR2DROracle is OCR2DRBillableAbstract, OCR2DROracleInterface, OCR2Base {
  event OracleRequest(bytes32 requestId, bytes data);
  event OracleResponse(bytes32 requestId);
  event UserCallbackError(bytes32 requestId, string reason);
  event UserCallbackRawError(bytes32 requestId, bytes lowLevelData);

  error EmptyRequestData();
  error InconsistentReportData();
  error EmptyPublicKey();

  bytes private s_donPublicKey;

  constructor() OCR2Base(true) {}

  /**
   * @notice The type and version of this contract
   * @return Type and version string
   */
  function typeAndVersion() external pure override returns (string memory) {
    return "OCR2DROracle 0.0.0";
  }

   /**
   * @inheritdoc OCR2DROracleInterface
   */
  function setRegistry(address registryAddress) external override ConfirmedOwner.onlyOwner {
    if (registryAddress == address(0)) {
      revert EmptyBillingRegistry();
    }
    OCR2DRBillableAbstract.s_registry = registryAddress;
  }

  /// @inheritdoc OCR2DROracleInterface
  function getDONPublicKey() external view override returns (bytes memory) {
    return s_donPublicKey;
  }

  /// @inheritdoc OCR2DROracleInterface
  function setDONPublicKey(bytes calldata donPublicKey) external override onlyOwner {
    if (donPublicKey.length == 0) {
      revert EmptyPublicKey();
    }
    s_donPublicKey = donPublicKey;
  }

  /// @inheritdoc OCR2DROracleInterface
  function sendRequest(OCR2DRRegistryInterface.RequestBilling calldata billing, bytes calldata data)
    external
    override
    returns (bytes32)
  {
    if (data.length == 0) {
      revert EmptyRequestData();
    }
    bytes32 requestId = OCR2DRRegistryInterface(OCR2DRBillableAbstract.s_registry).beginBilling(data, billing);
    emit OracleRequest(requestId, data);
    return requestId;
  }

  function _beforeSetConfig(uint8 _f, bytes memory _onchainConfig) internal override {}

  function _afterSetConfig(uint8 _f, bytes memory _onchainConfig) internal override {}

  function _validateReport(
    bytes32, /* configDigest */
    uint40, /* epochAndRound */
    bytes memory report
  ) internal pure override returns (bool) {
    bytes32[] memory requestIds;
    bytes[] memory results;
    bytes[] memory errors;
    (requestIds, results, errors) = abi.decode(report, (bytes32[], bytes[], bytes[]));
    if (requestIds.length != results.length && requestIds.length != errors.length) {
      return false;
    }
    return true;
  }

  function _report(
    uint32 initialGas,
    address transmitter,
    address[] memory signers,
    bytes calldata report
  ) internal override {
    bytes32[] memory requestIds;
    bytes[] memory results;
    bytes[] memory errors;
    (requestIds, results, errors) = abi.decode(report, (bytes32[], bytes[], bytes[]));
    for (uint256 i = 0; i < requestIds.length; i++) {
      try OCR2DRRegistryInterface(OCR2DRBillableAbstract.s_registry).concludeBilling(requestIds[i], results[i], errors[i], transmitter, signers, initialGas) {
        emit OracleResponse(requestIds[i]);
      } catch Error(string memory reason) {
        emit UserCallbackError(requestIds[i], reason);
      } catch (bytes memory lowLevelData) {
        emit UserCallbackRawError(requestIds[i], lowLevelData);
      }
    }
  }
}
